package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

// --- Config ---
const (
	ComposeFile      = "compose.yml"
	HiddenPortOffset = 1000
)

var (
	DefaultSettingPath = getEnv("ADMINER_TELEPORT_SETTING_PATH", "settings.json")
	DefaultOutputDir   = getEnv("ADMINER_TELEPORT_OUTPUT_DIR", "output")
)

type Database struct {
	Name        string `json:"name"`
	Cluster     string `json:"cluster"`
	DBSystem    string `json:"db_system"`
	DBUser      string `json:"db_user"`
	BridgePort  int    `json:"bridge_port"`
	AdminerPort int    `json:"adminer_port"`
	DBName      string `json:"db_name,omitempty"`
}

type Settings struct {
	Databases []Database `json:"databases"`
}

// --- Methods ---
func (d *Database) HiddenPort() int { return d.BridgePort + HiddenPortOffset }

func (d *Database) ServiceName() string {
	return regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(d.Name, "_")
}

func (d *Database) AdminerURL() string {
	driverMap := map[string]string{"pgsql": "pgsql", "mysql": "server"}
	driver, ok := driverMap[d.DBSystem]
	if !ok {
		driver = d.DBSystem
	}

	v := url.Values{}
	v.Set(driver, fmt.Sprintf("host.containers.internal:%d", d.BridgePort))
	v.Set("username", d.DBUser)
	if d.DBName != "" {
		v.Set("db", d.DBName)
	}

	return fmt.Sprintf("http://localhost:%d/?%s", d.AdminerPort, v.Encode())
}

func (d *Database) ToComposeService() map[string]any {
	return map[string]any{
		"image":   "adminer",
		"restart": "unless-stopped",
		"ports":   []string{fmt.Sprintf("%d:8080", d.AdminerPort)},
		"environment": map[string]any{
			"ADMINER_DESIGN":         "hever",
			"ADMINER_DEFAULT_SERVER": fmt.Sprintf("host.containers.internal:%d", d.BridgePort),
		},
		"volumes":     []string{"./plugins-enabled:/var/www/html/plugins-enabled:ro"},
		"extra_hosts": []string{"host.containers.internal:host-gateway"},
	}
}

// RunProxyTunnel starts the `tsh proxy db --tunnel` subprocess for this database.
// It builds the arguments from the database fields and delegates to
// `runLoggedCmd`, which ties the subprocess lifetime to the provided
// context (so cancellation will kill the child process).
func (d *Database) RunProxyTunnel(ctx context.Context, outputDir string) error {
	args := []string{"proxy", "db", "--tunnel", fmt.Sprintf("--port=%d", d.HiddenPort()), "--db-user=" + d.DBUser}
	if d.DBName != "" {
		args = append(args, "--db-name="+d.DBName)
	}
	args = append(args, d.Cluster)
	return runLoggedCmd(ctx, d.ProxyTunnelLogPath(), "tsh", args)
}

// RunSocat starts a socat bridge for this database, forwarding the
// public BridgePort to the hidden port used by the tsh tunnel.
func (d *Database) RunSocat(ctx context.Context, outputDir string) error {
	args := []string{
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr,bind=0.0.0.0", d.BridgePort),
		fmt.Sprintf("TCP:127.0.0.1:%d", d.HiddenPort()),
	}
	return runLoggedCmd(ctx, d.SocatLogPath(), "socat", args)
}

func (d *Database) ProxyTunnelLogPath() string {
	return filepath.Join(DefaultOutputDir, fmt.Sprintf("%s_tsh.log", d.Name))
}

func (d *Database) SocatLogPath() string {
	return filepath.Join(DefaultOutputDir, fmt.Sprintf("%s_socat.log", d.Name))
}

// --- Orchestrator ---

func runOrchestrator(ctx context.Context, selected []Database, outputDir string) (err error) {
	composeBase, err := detectComposeCmd()
	if err != nil {
		return fmt.Errorf("failed to detect compose command: %w", err)
	}

	err = os.RemoveAll(outputDir)
	if err != nil {
		return fmt.Errorf("failed to clear output dir: %w", err)
	}
	os.MkdirAll(outputDir, 0755)
	err = os.MkdirAll("plugins-enabled", 0755)
	if err != nil {
		return fmt.Errorf("failed to create plugins dir: %w", err)
	}

	// Sync Compose
	services := make(map[string]any)
	for _, db := range selected {
		services[db.ServiceName()] = db.ToComposeService()
	}
	composeData, err := yaml.Marshal(map[string]any{"services": services})
	if err != nil {
		return fmt.Errorf("failed to marshal compose data: %w", err)
	}

	err = os.WriteFile(ComposeFile, composeData, 0644)
	if err != nil {
		return fmt.Errorf("failed to write compose file: %w", err)
	}

	fmt.Printf("🚀 Starting Adminer via %s...\n", strings.Join(composeBase, " "))
	upArgs := append(composeBase, "-f", ComposeFile, "up", "-d")
	if out, err := exec.Command(upArgs[0], upArgs[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("compose up failed: %s", string(out))
	}

	defer func() {
		fmt.Println("🛑 Cleaning up...")
		downArgs := append(composeBase, "-f", ComposeFile, "down")
		err = exec.Command(downArgs[0], downArgs[1:]...).Run()
		if err != nil {
			fmt.Printf("❌ Compose down failed: %v\n", err)
		}
	}()

	// Create a cancellable child context and an errgroup bound to it.
	//
	// Purpose and flow:
	// - `childCtx` is derived from the parent `ctx` (which listens for
	//   OS signals). We cancel `childCtx` explicitly via `cancelGroup()`
	//   when any tunnel subprocess exits (successfully or with error).
	// - `errgroup.WithContext(childCtx)` returns `groupCtx` which is
	//   passed to each goroutine; `groupCtx` is cancelled when either:
	//     * `cancelGroup()` is called manually, or
	//     * any goroutine returns a non-nil error (errgroup cancels it),
	//     * the parent `ctx` is cancelled (OS signal).
	// - Subprocesses are started with `exec.CommandContext(groupCtx, ...)`
	//   inside `runLoggedCmd`. When `groupCtx` is cancelled, the
	//   underlying command receives a kill and exits, allowing the
	//   goroutines to return and `errgroup` to proceed to shutdown.
	//
	// Why we cancel on any tunnel exit:
	// The intended behavior is that the orchestrator should stop if
	// any essential tunnel (tsh or socat) exits. Calling `cancelGroup()`
	// from the tunnel goroutine ensures the other tunnels are torn
	// down and the Compose stack is brought down in the deferred cleanup.
	// This keeps the system in a deterministic state instead of leaving
	// half-open tunnels or running containers.
	childCtx, cancelGroup := context.WithCancel(ctx)
	defer cancelGroup()

	g, groupCtx := errgroup.WithContext(childCtx)

	for _, db := range selected {

		// 1. TSH Tunnel
		g.Go(func() error {
			err := db.RunProxyTunnel(groupCtx, outputDir)
			if err != nil {
				fmt.Printf("❌ [%s] TSH error: %v\n", db.Name, err)
				fmt.Printf("   Check %s.log for details\n", filepath.Join(outputDir, db.Name+"_tsh"))
			}
			cancelGroup()
			return err
		})

		// 2. SOCAT Bridge (Binding to 0.0.0.0 for container access)
		g.Go(func() error {
			fmt.Printf("🔗 [%s] -> %s\n", db.Name, db.AdminerURL())
			err := db.RunSocat(groupCtx, outputDir)
			if err != nil {
				fmt.Printf("❌ [%s] SOCAT error: %v\n", db.Name, err)
				fmt.Printf("   Check %s.log for details\n", filepath.Join(outputDir, db.Name+"_socat"))
			}
			cancelGroup()
			return err
		})
	}
	err = g.Wait()
	if err != nil {
		return err
	}

	return nil
}

func runLoggedCmd(ctx context.Context, logPath, bin string, args []string) (err error) {
	// Open a log file for the subprocess. Output (stdout/stderr) is
	// streamed to this file for debugging.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer func() {
		err = f.Close()
		if err != nil {
			fmt.Printf("❌ Failed to close log file: %v\n", err)
		}
	}()

	// Use exec.CommandContext so the started subprocess is tied to the
	// provided context: canceling the context will send a kill to the
	// child process, ensuring clean shutdown when we cancel `groupCtx`.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	return cmd.Run()
}

func detectComposeCmd() ([]string, error) {
	if err := exec.Command("docker", "compose", "version").Run(); err == nil {
		return []string{"docker", "compose"}, nil
	}
	if _, err := exec.LookPath("podman-compose"); err == nil {
		return []string{"podman-compose"}, nil
	}
	if _, err := exec.LookPath("docker-compose"); err == nil {
		return []string{"docker-compose"}, nil
	}
	return nil, fmt.Errorf("no container compose tool found")
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func main() {
	configPath := flag.String("config", DefaultSettingPath, "path to settings.json")
	outputDir := flag.String("out", DefaultOutputDir, "directory for logs")
	flag.Parse()

	content, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}

	var settings Settings
	if err := json.Unmarshal(content, &settings); err != nil {
		log.Fatalf("❌ JSON error: %v", err)
	}

	var selected []Database
	args := flag.Args()
	if len(args) > 0 {
		lookup := make(map[string]Database)
		for _, d := range settings.Databases {
			lookup[d.Name] = d
		}
		for _, name := range args {
			if db, ok := lookup[name]; ok {
				selected = append(selected, db)
			}
		}
	} else {
		selected = settings.Databases
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGKILL)
	defer cancel()

	if err := runOrchestrator(ctx, selected, *outputDir); err != nil && err != context.Canceled {
		fmt.Printf("❌ Fatal: %v\n", err)
		os.Exit(1)
	}

}

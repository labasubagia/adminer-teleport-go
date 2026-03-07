package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var driverMap = map[string]string{"pgsql": "pgsql", "mysql": "server"}

type Settings struct {
	Databases []Database `json:"databases"`
}

type Database struct {
	Name        string `json:"name"`
	Cluster     string `json:"cluster"`
	DBSystem    string `json:"db_system"`
	DBUser      string `json:"db_user"`
	BridgePort  int    `json:"bridge_port"`
	AdminerPort int    `json:"adminer_port"`
	DBName      string `json:"db_name,omitempty"`
}

func (d *Database) HiddenPort() int { return d.BridgePort + HiddenPortOffset }

func (d *Database) ServiceName() string {
	return regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(d.Name, "_")
}

func (d *Database) AdminerURL() string {
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

func (d *Database) ProxyTunnelLogPath() string {
	return filepath.Join(DefaultOutputDir, fmt.Sprintf("%s_tsh.log", d.Name))
}

func (d *Database) SocatLogPath() string {
	return filepath.Join(DefaultOutputDir, fmt.Sprintf("%s_socat.log", d.Name))
}

// RunProxyTunnel and RunSocat are methods that delegate to runLoggedCmd.
func (d *Database) RunProxyTunnel(ctx context.Context, outputDir string) error {
	args := []string{"proxy", "db", "--tunnel", fmt.Sprintf("--port=%d", d.HiddenPort()), "--db-user=" + d.DBUser}
	if d.DBName != "" {
		args = append(args, "--db-name="+d.DBName)
	}
	args = append(args, d.Cluster)
	return runLoggedCmd(ctx, d.ProxyTunnelLogPath(), "tsh", args)
}

func (d *Database) RunSocat(ctx context.Context, outputDir string) error {
	args := []string{
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr,bind=0.0.0.0", d.BridgePort),
		fmt.Sprintf("TCP:127.0.0.1:%d", d.HiddenPort()),
	}
	return runLoggedCmd(ctx, d.SocatLogPath(), "socat", args)
}

// Validate checks that required fields are present, ports are valid,
// and the DB system is supported.
func (d *Database) Validate() error {
	portUpperBound := 65535
	bridePortUpperBound := portUpperBound - HiddenPortOffset

	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(d.Cluster) == "" {
		return fmt.Errorf("cluster is required for %s", d.Name)
	}
	if strings.TrimSpace(d.DBSystem) == "" {
		return fmt.Errorf("db_system is required for %s", d.Name)
	}
	if strings.TrimSpace(d.DBUser) == "" {
		return fmt.Errorf("db_user is required for %s", d.Name)
	}
	if _, ok := driverMap[d.DBSystem]; !ok {
		return fmt.Errorf("unsupported db_system %q for %s", d.DBSystem, d.Name)
	}
	if d.BridgePort <= 0 || d.BridgePort > portUpperBound {
		return fmt.Errorf("bridge_port must be between 1 and 65535 for %s", d.Name)
	}
	if d.AdminerPort <= 0 || d.AdminerPort > portUpperBound {
		return fmt.Errorf("adminer_port must be between 1 and 65535 for %s", d.Name)
	}
	if d.AdminerPort == d.BridgePort {
		return fmt.Errorf("adminer_port must differ from bridge_port for %s", d.Name)
	}
	// Validate hidden port (bridge + offset)
	hidden := d.HiddenPort()
	if hidden <= 0 || hidden > portUpperBound {
		return fmt.Errorf("hidden port must be between 1 and 65535m for %s (got %d). use bridge port between 1-%d", d.Name, hidden, bridePortUpperBound)
	}
	if hidden == d.BridgePort || hidden == d.AdminerPort {
		return fmt.Errorf("hidden port (%d) conflicts with bridge or adminer port for %s", hidden, d.Name)
	}
	return nil
}

// LoadSelectedDatabases reads the JSON config at configPath, unmarshals it,
// filters databases according to selectedNames (if non-empty) and validates
// each selected database. Returns the selected slice or an aggregated error.
func LoadSelectedDatabases(configPath string, selectedNames []string) ([]Database, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var settings Settings
	if err := json.Unmarshal(content, &settings); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	var selected []Database
	if len(selectedNames) > 0 {
		lookup := make(map[string]Database)
		for _, d := range settings.Databases {
			lookup[d.Name] = d
		}
		for _, name := range selectedNames {
			if db, ok := lookup[name]; ok {
				selected = append(selected, db)
			}
		}
	} else {
		selected = settings.Databases
	}

	var errs []string
	for _, db := range selected {
		if err := db.Validate(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", db.Name, err.Error()))
		}
	}

	// Check for port duplication across selected databases (bridge, adminer, hidden)
	portOwners := make(map[int][]string)
	for _, db := range selected {
		portOwners[db.BridgePort] = append(portOwners[db.BridgePort], fmt.Sprintf("%s(bridge)", db.Name))
		portOwners[db.AdminerPort] = append(portOwners[db.AdminerPort], fmt.Sprintf("%s(adminer)", db.Name))
		hidden := db.HiddenPort()
		portOwners[hidden] = append(portOwners[hidden], fmt.Sprintf("%s(hidden)", db.Name))
	}
	for port, owners := range portOwners {
		if len(owners) > 1 {
			errs = append(errs, fmt.Sprintf("port %d is used by multiple databases: %s", port, strings.Join(owners, ", ")))
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return selected, nil
}

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

type ErrValidation struct {
	Errs []string
}

func (e *ErrValidation) Add(msg string) {
	e.Errs = append(e.Errs, msg)
}

func (e *ErrValidation) Error() string {
	return strings.Join(e.Errs, "; ")
}

var driverMap = map[string]string{"pgsql": "pgsql", "mysql": "server"}

type Settings struct {
	Databases []Database `json:"databases"`
}

func (s *Settings) Validate() error {
	var ve ErrValidation
	add := func(s string) { ve.Add(s) }

	// Basic validation for the settings struct itself
	if len(s.Databases) == 0 {
		add("at least one database configuration is required")
	}

	// Check for port duplication across selected databases (bridge, adminer, hidden)
	portOwners := make(map[int][]string)
	for _, db := range s.Databases {
		portOwners[db.BridgePort] = append(portOwners[db.BridgePort], fmt.Sprintf("%s(bridge)", db.Name))
		portOwners[db.AdminerPort] = append(portOwners[db.AdminerPort], fmt.Sprintf("%s(adminer)", db.Name))
		hidden := db.HiddenPort()
		portOwners[hidden] = append(portOwners[hidden], fmt.Sprintf("%s(hidden)", db.Name))
	}
	for port, owners := range portOwners {
		if len(owners) > 1 {
			add(fmt.Sprintf("port %d is used by multiple databases: %s", port, strings.Join(owners, ", ")))
		}
	}

	if len(ve.Errs) > 0 {
		return &ve
	}

	return nil
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

// RunProxyTunnel and RunSocat delegate to runLoggedCmd.
// If outputDir is non-empty it will be used for the log path instead of DefaultOutputDir.
func (d *Database) RunProxyTunnel(ctx context.Context, outputDir string) error {
	logPath := d.ProxyTunnelLogPath()
	if strings.TrimSpace(outputDir) != "" {
		logPath = filepath.Join(outputDir, fmt.Sprintf("%s_tsh.log", d.Name))
	}
	args := []string{"proxy", "db", "--tunnel", fmt.Sprintf("--port=%d", d.HiddenPort()), "--db-user=" + d.DBUser}
	if d.DBName != "" {
		args = append(args, "--db-name="+d.DBName)
	}
	args = append(args, d.Cluster)
	return runLoggedCmd(ctx, logPath, "tsh", args)
}

func (d *Database) RunSocat(ctx context.Context, outputDir string) error {
	logPath := d.SocatLogPath()
	if strings.TrimSpace(outputDir) != "" {
		logPath = filepath.Join(outputDir, fmt.Sprintf("%s_socat.log", d.Name))
	}
	args := []string{
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr,bind=0.0.0.0", d.BridgePort),
		fmt.Sprintf("TCP:127.0.0.1:%d", d.HiddenPort()),
	}
	return runLoggedCmd(ctx, logPath, "socat", args)
}

// Validate checks that required fields are present, ports are valid,
// and the DB system is supported.
func (d *Database) Validate() error {
	var ve ErrValidation
	add := func(s string) { ve.Add(s) }

	if strings.TrimSpace(d.Name) == "" {
		add("name is required")
	}
	if strings.TrimSpace(d.Cluster) == "" {
		add("cluster is required")
	}
	if strings.TrimSpace(d.DBSystem) == "" {
		add("db_system is required")
	}
	if strings.TrimSpace(d.DBUser) == "" {
		add("db_user is required")
	}
	if _, ok := driverMap[d.DBSystem]; !ok {
		add(fmt.Sprintf("unsupported db_system '%s'", d.DBSystem))
	}
	if d.BridgePort <= 0 || d.BridgePort > BridgePortUpperBound {
		add(fmt.Sprintf("bridge_port must be between 1 and %d", BridgePortUpperBound))
	}
	if d.AdminerPort <= 0 || d.AdminerPort > PortUpperBound {
		add(fmt.Sprintf("adminer_port must be between 1 and %d", PortUpperBound))
	}
	if d.AdminerPort == d.BridgePort {
		add(fmt.Sprintf("adminer_port must differ from bridge_port for %s", d.Name))
	}

	hidden := d.HiddenPort()
	if hidden <= 0 || hidden > PortUpperBound {
		add(fmt.Sprintf("hidden port must be between 1 and %d for %s (got %d). use bridge port between 1-%d", PortUpperBound, d.Name, hidden, BridgePortUpperBound))
	}
	if hidden == d.BridgePort || hidden == d.AdminerPort {
		add(fmt.Sprintf("hidden port (%d) conflicts with bridge or adminer port for %s", hidden, d.Name))
	}

	// Check ports are available on the host
	for _, p := range []struct {
		port int
		name string
	}{
		{d.BridgePort, "bridge_port"},
		{d.AdminerPort, "adminer_port"},
		{hidden, "hidden port"},
	} {
		if !isPortAvailable(p.port) {
			add(fmt.Sprintf("%s %d is already in use on host for %s", p.name, p.port, d.Name))
		}
	}

	if len(ve.Errs) > 0 {
		return &ve
	}
	return nil
}

// LoadSelectedDatabases reads the JSON config at configPath, unmarshals it,
// filters databases according to selectedNames (if non-empty) and validates
// each selected database. Returns the selected slice or an aggregated error.
func LoadSelectedDatabases(configPath string, selectedNames []string) ([]Database, error) {

	settings, err := loadSettings(configPath)
	if err != nil {
		return nil, err
	}

	var selected []Database
	var missing []string
	if len(selectedNames) > 0 {
		lookup := make(map[string]Database, len(settings.Databases))
		for _, d := range settings.Databases {
			lookup[d.Name] = d
		}
		for _, name := range selectedNames {
			if db, ok := lookup[name]; ok {
				selected = append(selected, db)
			} else {
				missing = append(missing, name)
			}
		}
	} else {
		selected = settings.Databases
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the following database names were not found in config: %s", strings.Join(missing, ", "))
	}

	var errs []string
	for i, db := range selected {
		if err := db.Validate(); err != nil {
			if ev, ok := err.(*ErrValidation); ok {
				errs = append(errs, fmt.Sprintf("json data database-%d %s: \n\t- %s", i, db.Name, strings.Join(ev.Errs, "\n\t- ")))
			} else {
				errs = append(errs, fmt.Sprintf("json data database-%d %s: %s", i, db.Name, err.Error()))
			}
		}
	}

	return selected, nil
}

func loadSettings(configPath string) (*Settings, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config JSON: %w", err)
	}
	err = s.Validate()
	if err != nil {
		if ev, ok := err.(*ErrValidation); ok {
			return nil, fmt.Errorf("config validation error: \n\t- %s", strings.Join(ev.Errs, "\n\t- "))
		}
		return nil, fmt.Errorf("config validation error: %w", err)
	}
	return &s, nil
}

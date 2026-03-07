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

func (e *ErrValidation) Error() string {
	return strings.Join(e.Errs, "; ")
}

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

	errs := []string{}

	if strings.TrimSpace(d.Name) == "" {
		errs = append(errs, "name is required")
	}
	if strings.TrimSpace(d.Cluster) == "" {
		errs = append(errs, "cluster is required")
	}
	if strings.TrimSpace(d.DBSystem) == "" {
		errs = append(errs, "db_system is required")
	}
	if strings.TrimSpace(d.DBUser) == "" {
		errs = append(errs, "db_user is required")
	}
	if _, ok := driverMap[d.DBSystem]; !ok {
		errs = append(errs, fmt.Sprintf("unsupported db_system '%s'", d.DBSystem))
	}
	if d.BridgePort <= 0 || d.BridgePort > BridgePortUpperBound {
		errs = append(errs, fmt.Sprintf("bridge_port must be between 1 and %d", BridgePortUpperBound))
	}
	if d.AdminerPort <= 0 || d.AdminerPort > PortUpperBound {
		errs = append(errs, fmt.Sprintf("adminer_port must be between 1 and %d", PortUpperBound))
	}
	if d.AdminerPort == d.BridgePort {
		errs = append(errs, fmt.Sprintf("adminer_port must differ from bridge_port for %s", d.Name))
	}
	// Validate hidden port (bridge + offset)
	hidden := d.HiddenPort()
	if hidden <= 0 || hidden > PortUpperBound {
		errs = append(errs, fmt.Sprintf("hidden port must be between 1 and %d for %s (got %d). use bridge port between 1-%d", PortUpperBound, d.Name, hidden, BridgePortUpperBound))
	}
	if hidden == d.BridgePort || hidden == d.AdminerPort {
		errs = append(errs, fmt.Sprintf("hidden port (%d) conflicts with bridge or adminer port for %s", hidden, d.Name))
	}

	// Check ports are available on the host
	if !isPortAvailable(d.BridgePort) {
		errs = append(errs, fmt.Sprintf("bridge_port %d is already in use on host for %s", d.BridgePort, d.Name))
	}
	if !isPortAvailable(d.AdminerPort) {
		errs = append(errs, fmt.Sprintf("adminer_port %d is already in use on host for %s", d.AdminerPort, d.Name))
	}
	if !isPortAvailable(hidden) {
		errs = append(errs, fmt.Sprintf("hidden port %d is already in use on host for %s", hidden, d.Name))
	}
	if len(errs) > 0 {
		return &ErrValidation{Errs: errs}
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
	invalidNames := []string{}
	if len(selectedNames) > 0 {
		lookup := make(map[string]Database)
		for _, d := range settings.Databases {
			lookup[d.Name] = d
		}
		for _, name := range selectedNames {
			if db, ok := lookup[name]; ok {
				selected = append(selected, db)
			} else {
				invalidNames = append(invalidNames, name)
			}
		}
	} else {
		selected = settings.Databases
	}
	if len(invalidNames) > 0 {
		return nil, fmt.Errorf("the following database names were not found in config: %s", strings.Join(invalidNames, ", "))
	}

	var errs []string
	for i, db := range selected {
		if err := db.Validate(); err != nil {
			errVal, ok := err.(*ErrValidation)
			var errMsg string
			if ok {
				errMsg = fmt.Sprintf("\n\t- %s", strings.Join(errVal.Errs, "\n\t- "))
			} else {
				errMsg = err.Error()
			}
			errs = append(errs, fmt.Sprintf("json data database-%d %s: %s", i, db.Name, errMsg))
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
		return nil, fmt.Errorf("\n- %s", strings.Join(errs, "\n- "))
	}
	return selected, nil
}

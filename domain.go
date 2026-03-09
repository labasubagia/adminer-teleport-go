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

	"gopkg.in/yaml.v3"
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

func LoadSettings(configPath string) (*Settings, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config JSON: %w", err)
	}
	err = s.validate()
	if err != nil {
		if ev, ok := err.(*ErrValidation); ok {
			return nil, fmt.Errorf("config validation error: %s", fmt.Errorf("\n\t- %s", strings.Join(ev.Errs, "\n\t- ")))
		}
		return nil, fmt.Errorf("config validation error: %w", err)
	}
	return &s, nil
}

func (s *Settings) validate() error {
	var ev ErrValidation
	add := func(s string) { ev.Add(s) }

	if len(s.Databases) == 0 {
		add("at least one database configuration is required")
	}

	for _, db := range s.Databases {
		if err := db.Validate(); err != nil {
			if ev, ok := err.(*ErrValidation); ok {
				add(fmt.Sprintf("%s: %s", db.Name, fmt.Sprintf("\n\t\t- %s", strings.Join(ev.Errs, "\n\t\t- "))))
			} else {
				add(fmt.Sprintf("%s: %v", db.Name, err))
			}
		}
	}

	if err := s.checkDbPorts(); err != nil {
		if ev, ok := err.(*ErrValidation); ok {
			add(fmt.Sprintf("port configuration errors: %s", fmt.Sprintf("\n\t\t- %s", strings.Join(ev.Errs, "\n\t\t- "))))
		} else {
			add(fmt.Sprintf("port configuration error: %v", err))
		}
	}

	if len(ev.Errs) > 0 {
		return &ev
	}

	return nil
}

// checkDbPorts checks for port conflicts across all databases in the settings.
//
// Tunnel port needs to be unique across all databases,
// and must not conflict with any Adminer ports.
//
// Duplicates adminer port means one adminer instance can be used for multiple databases,
// adminer port duplicates are allowerd and require re-login when switching between databases.
func (s *Settings) checkDbPorts() error {
	var ev ErrValidation
	add := func(s string) { ev.Add(s) }

	tunnelPortMap := make(map[int][]string)
	adminerPortMap := make(map[int]struct{})
	for _, db := range s.Databases {
		tunnelPortMap[db.BridgePort] = append(tunnelPortMap[db.BridgePort], fmt.Sprintf("%s(bridge)", db.Name))
		hidden := db.HiddenPort()
		tunnelPortMap[hidden] = append(tunnelPortMap[hidden], fmt.Sprintf("%s(hidden)", db.Name))

		adminerPortMap[db.AdminerPort] = struct{}{}
	}
	// Check for any ports that are used by multiple databases or that conflict with Adminer ports.
	for port, owners := range tunnelPortMap {
		if len(owners) > 1 {
			add(fmt.Sprintf("port %d is used by multiple databases: %s", port, strings.Join(owners, ", ")))
		}

		if _, ok := adminerPortMap[port]; ok {
			add(fmt.Sprintf("port %d is used as an Adminer port", port))
		}
	}

	if len(ev.Errs) > 0 {
		return &ev
	}

	return nil
}

// GenerateComposeFile generates a Docker Compose YAML file at the given path
// containing service entries for the provided databases.
//
// Databases that share the same Adminer port will be served by a single
// Adminer instance (service key "adminer_<port>") — this allows Adminer to be
// reused but requires re-login when switching between databases.
//
// Databases that use unique Adminer ports get their own Adminer service (service key
// from Database.ServiceName()); standalone Adminer instances are used to
// allow concurrent login sessions to multiple database clusters.
func (s *Settings) GenerateComposeFile(dbs []Database, path string) error {
	services := make(map[string]any)

	// To allow multiple databases to share the same Adminer instance when they use the same Adminer port,
	// but need to re-login when switching between clusters,
	// it is created when multiple databases share the same adminer port,
	// otherwise each database will have its own adminer instance.
	sharedAdminer := make(map[int][]Database)
	for _, db := range dbs {
		sharedAdminer[db.AdminerPort] = append(sharedAdminer[db.AdminerPort], db)
	}
	for port, dbList := range sharedAdminer {
		if len(dbList) > 1 {
			services[fmt.Sprintf("adminer_%d", port)] = dbList[0].ToComposeService()
		}
	}

	// Standalone adminer services are created for each unique Adminer port across all databases.
	// This allows multiple databases to share an Adminer instance when they use the same Adminer port,
	// and allows concurrent login sessions
	for _, db := range dbs {
		if len(sharedAdminer[db.AdminerPort]) > 1 {
			continue
		}
		services[db.ServiceName()] = db.ToComposeService()
	}

	composeData, err := yaml.Marshal(map[string]any{"services": services})
	if err != nil {
		return fmt.Errorf("failed to marshal compose data: %w", err)
	}
	err = os.WriteFile(path, composeData, 0644)
	if err != nil {
		return fmt.Errorf("failed to write compose file: %w", err)
	}
	return nil
}

func (s *Settings) FilterDatabases(selectedNames []string) ([]Database, error) {

	if len(selectedNames) == 0 {
		return s.Databases, nil
	}

	var selected []Database
	var missing []string
	lookup := make(map[string]Database, len(s.Databases))
	for _, d := range s.Databases {
		lookup[d.Name] = d
	}
	for _, name := range selectedNames {
		if db, ok := lookup[name]; ok {
			selected = append(selected, db)
		} else {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("the following database names were not found in config: %s", strings.Join(missing, ", "))
	}

	return selected, nil
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

func (d *Database) ServiceName() string {
	return regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(d.Name, "_")
}

// RunProxyTunnel starts a tsh proxy tunnel for the database and logs tsh output.
//
// It launches "tsh proxy db --tunnel ..." (including --port set to the database's hidden port,
// --db-user and, when provided, --db-name) and writes logs to the database's proxy tunnel log path
// or to outputDir when a non-empty path is supplied.
//
// Why use a proxy tunnel:
//   - Directly using the proxy can require establishing SSL connections to the database, which is tedious;
//     the tunnel simplifies client connectivity.
//   - A hidden port is used because Adminer cannot access the loopback interface directly (127.0.0.1);
//
// /    that loopback access is mapped via socat to expose the tunnel to Adminer.
//   - Some database drivers (e.g., Postgres) require an explicit database name argument (--db-name), so
//     DBName is forwarded when set.
//
// Returns an error if starting the tsh process or writing logs fails.
func (d *Database) RunProxyTunnel(ctx context.Context, outputDir string) error {
	logPath := d.ProxyTunnelLogPath(outputDir)
	args := []string{"proxy", "db", "--tunnel", fmt.Sprintf("--port=%d", d.HiddenPort()), "--db-user=" + d.DBUser}
	if d.DBName != "" {
		args = append(args, "--db-name="+d.DBName)
	}
	args = append(args, d.Cluster)
	return runLoggedCmd(ctx, logPath, "tsh", args)
}

func (d *Database) ProxyTunnelLogPath(outputDir string) string {
	return filepath.Join(outputDir, fmt.Sprintf("%s_tsh.log", d.Name))
}

// RunSocat starts a socat process that listens on the database BridgePort on 0.0.0.0
// and forwards all incoming TCP connections to the database's local hidden port (127.0.0.1:<hidden>).
// This enables tools running in the adminer/container network to reach services bound to the host loopback.
//
// Why socat is used:
//   - The adminer network cannot access the host's 127.0.0.1 loopback directly (tsh enforces loopback isolation).
//   - Adminer needs an address reachable from containers (e.g. host.containers.internal -> 0.0.0.0).
//   - socat bridges that gap by listening on 0.0.0.0 and forwarding requests to 127.0.0.1, allowing containerized
//     clients to access services bound to the host loopback.
func (d *Database) RunSocat(ctx context.Context, outputDir string) error {
	logPath := d.SocatLogPath(outputDir)
	args := []string{
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr,bind=0.0.0.0", d.BridgePort),
		fmt.Sprintf("TCP:127.0.0.1:%d", d.HiddenPort()),
	}
	return runLoggedCmd(ctx, logPath, "socat", args)
}

func (d *Database) SocatLogPath(outputDir string) string {
	return filepath.Join(outputDir, fmt.Sprintf("%s_socat.log", d.Name))
}

func (d *Database) HiddenPort() int { return d.BridgePort + HiddenPortOffset }

// Validate checks that required fields are present, ports are valid,
// and the DB system is supported.
func (d *Database) Validate() error {
	var ev ErrValidation
	add := func(s string) { ev.Add(s) }

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
		add(fmt.Sprintf(
			"hidden_port must be between 1 and %d for %s (got %d). Please use bridge_port between 1-%d",
			PortUpperBound, d.Name, hidden, BridgePortUpperBound,
		))
	}
	if hidden == d.BridgePort || hidden == d.AdminerPort {
		add(fmt.Sprintf("hidden_port (%d) conflicts with bridge_port or adminer_port for %s", hidden, d.Name))
	}

	// Check ports are available on the host
	for _, p := range []struct {
		port int
		name string
	}{
		{d.BridgePort, "bridge_port"},
		{hidden, "hidden_port"},
	} {
		if !isPortAvailable(p.port) {
			add(fmt.Sprintf("%s %d is already in use on host for %s", p.name, p.port, d.Name))
		}
	}

	if len(ev.Errs) > 0 {
		return &ev
	}
	return nil
}

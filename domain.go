package main

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
)

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

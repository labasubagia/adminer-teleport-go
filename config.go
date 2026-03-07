package main

import "os"

// --- Config ---
const (
	ComposeFile      = "compose.yml"
	HiddenPortOffset = 1000
)

var (
	DefaultSettingPath = getEnv("ADMINER_TELEPORT_SETTING_PATH", "settings.json")
	DefaultOutputDir   = getEnv("ADMINER_TELEPORT_OUTPUT_DIR", "output")
)

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

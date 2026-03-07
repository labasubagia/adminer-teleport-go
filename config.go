package main

// --- Config ---
const (
	ComposeFile          = "compose.yml"
	HiddenPortOffset     = 1000
	PortUpperBound       = 65535
	BridgePortUpperBound = PortUpperBound - HiddenPortOffset
)

var (
	DefaultSettingPath = getEnv("ADMINER_TELEPORT_SETTING_PATH", "settings.json")
	DefaultOutputDir   = getEnv("ADMINER_TELEPORT_OUTPUT_DIR", "output")
)

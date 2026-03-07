package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// parseSelectedNames takes the command-line arguments and extracts database names,
// allowing for comma-separated lists in each argument.
// For example, if the user runs `adminer-teleport db1,db2 db3`,
// this function will return a slice containing ["db1", "db2", "db3"].
func parseSelectedNames(args []string) []string {
	var selected []string
	for _, arg := range args {
		for _, name := range strings.Split(arg, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				selected = append(selected, name)
			}
		}
	}
	return selected
}

func main() {
	configPath := flag.String("config", DefaultSettingPath, "path to settings.json")
	outputDir := flag.String("out", DefaultOutputDir, "directory for logs")
	flag.Parse()

	selectedNames := parseSelectedNames(flag.Args())

	// Set up signal handling to allow graceful shutdown on interrupt (Ctrl+C) or termination signals.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runOrchestrator(ctx, *configPath, selectedNames, *outputDir); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			fmt.Println("⚠️  Orchestrator stopped gracefully.")
			os.Exit(0)
		}
		fmt.Printf("❌ Fatal: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Orchestrator completed successfully.")
}

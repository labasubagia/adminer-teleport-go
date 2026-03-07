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

func main() {
	configPath := flag.String("config", DefaultSettingPath, "path to settings.json")
	outputDir := flag.String("out", DefaultOutputDir, "directory for logs")
	flag.Parse()

	args := flag.Args()
	selectedNames := []string{}
	for _, arg := range args {
		names := strings.Split(arg, ",")
		for _, name := range names {
			selectedNames = append(selectedNames, strings.TrimSpace(name))
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	err := runOrchestrator(ctx, *configPath, selectedNames, *outputDir)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			fmt.Println("⚠️  Orchestrator stopped gracefully.")
			os.Exit(0)
		}
		fmt.Printf("❌ Fatal: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Orchestrator completed successfully.")
	os.Exit(0)
}

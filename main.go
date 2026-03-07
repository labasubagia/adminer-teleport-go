package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	configPath := flag.String("config", DefaultSettingPath, "path to settings.json")
	outputDir := flag.String("out", DefaultOutputDir, "directory for logs")
	flag.Parse()
	args := flag.Args()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGKILL)
	defer cancel()

	if err := runOrchestrator(ctx, *configPath, args, *outputDir); err != nil && err != context.Canceled {
		fmt.Printf("❌ Fatal: %v\n", err)
		os.Exit(1)
	}
}

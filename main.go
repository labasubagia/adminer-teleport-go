package main

import (
	"context"
	"errors"
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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := runOrchestrator(ctx, *configPath, args, *outputDir); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		fmt.Printf("❌ Fatal: %v\n", err)
		os.Exit(1)
	}
}

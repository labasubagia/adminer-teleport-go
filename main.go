package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	configPath := flag.String("config", DefaultSettingPath, "path to settings.json")
	outputDir := flag.String("out", DefaultOutputDir, "directory for logs")
	flag.Parse()

	content, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}

	var settings Settings
	if err := json.Unmarshal(content, &settings); err != nil {
		log.Fatalf("❌ JSON error: %v", err)
	}

	var selected []Database
	args := flag.Args()
	if len(args) > 0 {
		lookup := make(map[string]Database)
		for _, d := range settings.Databases {
			lookup[d.Name] = d
		}
		for _, name := range args {
			if db, ok := lookup[name]; ok {
				selected = append(selected, db)
			}
		}
	} else {
		selected = settings.Databases
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGKILL)
	defer cancel()

	if err := runOrchestrator(ctx, selected, *outputDir); err != nil && err != context.Canceled {
		fmt.Printf("❌ Fatal: %v\n", err)
		os.Exit(1)
	}
}

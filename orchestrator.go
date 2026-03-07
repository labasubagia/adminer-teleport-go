package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

func runOrchestrator(ctx context.Context, configPath string, selectedNames []string, outputDir string) (err error) {
	// Verify external prerequisites first
	if err := CheckPrerequisites(); err != nil {
		return err
	}

	selected, err := LoadSelectedDatabases(configPath, selectedNames)
	if err != nil {
		return err
	}

	composeBase, err := detectComposeCmd()
	if err != nil {
		return fmt.Errorf("failed to detect compose command: %w", err)
	}

	err = os.RemoveAll(outputDir)
	if err != nil {
		return fmt.Errorf("failed to clear output dir: %w", err)
	}
	os.MkdirAll(outputDir, 0755)
	err = os.MkdirAll("plugins-enabled", 0755)
	if err != nil {
		return fmt.Errorf("failed to create plugins dir: %w", err)
	}

	services := make(map[string]any)
	for _, db := range selected {
		services[db.ServiceName()] = db.ToComposeService()
	}
	composeData, err := yaml.Marshal(map[string]any{"services": services})
	if err != nil {
		return fmt.Errorf("failed to marshal compose data: %w", err)
	}

	err = os.WriteFile(ComposeFile, composeData, 0644)
	if err != nil {
		return fmt.Errorf("failed to write compose file: %w", err)
	}

	fmt.Printf("🚀 Starting Adminer via %s...\n", strings.Join(composeBase, " "))
	upArgs := append(composeBase, "-f", ComposeFile, "up", "-d")
	if out, err := exec.Command(upArgs[0], upArgs[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("compose up failed: %s", string(out))
	}

	defer func() {
		fmt.Println("🛑 Cleaning up...")
		downArgs := append(composeBase, "-f", ComposeFile, "down")
		err = exec.Command(downArgs[0], downArgs[1:]...).Run()
		if err != nil {
			fmt.Printf("❌ Compose down failed: %v\n", err)
		}
	}()

	childCtx, cancelGroup := context.WithCancel(ctx)
	defer cancelGroup()

	g, groupCtx := errgroup.WithContext(childCtx)

	for _, db := range selected {
		g.Go(func() error {
			err := db.RunProxyTunnel(groupCtx, outputDir)
			if err != nil {
				fmt.Printf("❌ [%s] TSH error: %v\n", db.Name, err)
				fmt.Printf("   Check %s for details\n", db.ProxyTunnelLogPath())
			}
			cancelGroup()
			return err
		})

		g.Go(func() error {
			fmt.Printf("🔗 [%s] -> %s\n", db.Name, db.AdminerURL())
			err := db.RunSocat(groupCtx, outputDir)
			if err != nil {
				fmt.Printf("❌ [%s] SOCAT error: %v\n", db.Name, err)
				fmt.Printf("   Check %s for details\n", db.SocatLogPath())
			}
			cancelGroup()
			return err
		})
	}
	err = g.Wait()
	if err != nil {
		return err
	}

	return nil
}

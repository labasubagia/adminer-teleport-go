package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func runLoggedCmd(ctx context.Context, logPath, bin string, args []string) (err error) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer func() {
		err = f.Close()
		if err != nil {
			fmt.Printf("❌ Failed to close log file: %v\n", err)
		}
	}()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	return cmd.Run()
}

func detectComposeCmd() ([]string, error) {
	if err := exec.Command("docker", "compose", "version").Run(); err == nil {
		return []string{"docker", "compose"}, nil
	}
	if _, err := exec.LookPath("podman-compose"); err == nil {
		return []string{"podman-compose"}, nil
	}
	if _, err := exec.LookPath("docker-compose"); err == nil {
		return []string{"docker-compose"}, nil
	}
	return nil, fmt.Errorf("no container compose tool found")
}

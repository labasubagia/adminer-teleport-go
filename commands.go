package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// runLoggedCmd executes an external process while capturing its stdout/stderr to a persistent log file.
// The primary purpose is operational observability and post-mortem debugging: having a single, durable
// log for each invocation makes it much easier to inspect failures that occur in CI, automation, or
// long-running admin tasks, and ensures logs survive process termination and context cancellation.
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

// detectComposeCmd inspects the host to select a usable container compose tooling variant.
// The rationale is portability and resilience: different environments (developer machines, CI runners,
// diverse Linux distributions) may expose "podman compose", "podman-compose", "docker compose" or the
// legacy "docker-compose". Discovering the available implementation at runtime avoids hard assumptions
// and lets the same workflow run across heterogeneous systems.
func detectComposeCmd() ([]string, error) {
	if err := exec.Command("podman", "compose", "version").Run(); err == nil {
		return []string{"podman", "compose"}, nil
	}
	if _, err := exec.LookPath("podman-compose"); err == nil {
		return []string{"podman-compose"}, nil
	}
	if err := exec.Command("docker", "compose", "version").Run(); err == nil {
		return []string{"docker", "compose"}, nil
	}
	if _, err := exec.LookPath("docker-compose"); err == nil {
		return []string{"docker-compose"}, nil
	}
	return nil, fmt.Errorf("no container compose tool found")
}

// composeUpCmd brings up the compose services using the specified compose command and file.
func composeUpCmd(composeBase []string, composeFile string) error {
	args := append(composeBase, "-f", composeFile, "up", "-d")
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("compose up failed: %s", string(out))
	}
	return nil
}

// composeDownCmd brings down the compose services using the specified compose command and file.
func composeDownCmd(composeBase []string, composeFile string) error {
	args := append(composeBase, "-f", composeFile, "down")
	if err := exec.Command(args[0], args[1:]...).Run(); err != nil {
		return fmt.Errorf("compose down failed: %w", err)
	}
	return nil
}

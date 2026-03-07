package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// CheckPrerequisites verifies required external tools are present and usable.
func CheckPrerequisites() error {
	var errs []string

	// Compose tool
	if _, err := detectComposeCmd(); err != nil {
		errs = append(errs, "no container compose tool found (docker compose, podman-compose, or docker-compose)")
	}

	// tsh (Teleport client)
	if _, err := exec.LookPath("tsh"); err != nil {
		errs = append(errs, "tsh (Teleport) not installed or not in PATH")
	} else {
		// Check logged-in status
		out, err := exec.Command("tsh", "status").CombinedOutput()
		if err != nil {
			s := strings.TrimSpace(string(out))
			if s == "" {
				s = err.Error()
			}
			errs = append(errs, fmt.Sprintf("tsh not logged in or unable to contact auth server: %s", s))
		}
	}

	// socat
	if _, err := exec.LookPath("socat"); err != nil {
		errs = append(errs, "socat is not installed or not in PATH")
	}

	if len(errs) > 0 {
		return fmt.Errorf("\n- %s", strings.Join(errs, "\n- "))
	}
	return nil
}

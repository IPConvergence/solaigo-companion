//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// The task name under which this binary is registered. Visible in Task
// Scheduler; `uninstall` removes it by name.
const scheduledTaskName = "SolaigoCompanion"

func installService(out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not resolve the current binary path: %w", err)
	}
	// A Scheduled Task triggered at logon, running under the current user.
	// `schtasks` is the built-in command Windows ships with; no third-party
	// tooling needed. The Task Scheduler service enforces run-at-logon and
	// restart-on-crash via the task settings, same role systemd and launchd
	// play on the other OSes.
	cmd := exec.Command("schtasks",
		"/create",
		"/tn", scheduledTaskName,
		"/sc", "onlogon",
		"/tr", fmt.Sprintf(`"%s" daemon`, exe),
		"/rl", "limited",
		// /f overwrites a prior registration if there is one; without it a
		// repeat install stops at a prompt.
		"/f",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("schtasks /create: %w", err)
	}
	fmt.Fprintln(out, "Registered scheduled task:", scheduledTaskName)
	fmt.Fprintln(out, "Start it now with: schtasks /run /tn", scheduledTaskName)
	fmt.Fprintln(out, "It will start automatically at next logon.")
	return nil
}

func uninstallService(out io.Writer) error {
	cmd := exec.Command("schtasks", "/delete", "/tn", scheduledTaskName, "/f")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// schtasks /delete returns non-zero when the task is not found; treat as
	// soft success so a repeat uninstall is idempotent.
	_ = cmd.Run()
	fmt.Fprintln(out, "Removed scheduled task (if it existed):", scheduledTaskName)
	return nil
}

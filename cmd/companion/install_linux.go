//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Where the systemd user unit lives for the current user. The .config path is
// XDG-standard; systemd reads it with `--user`.
func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "systemd", "user", "solaigo-companion.service"), nil
}

func unitContent(exe string) string {
	// Restart on failure (crash, cockpit outage), not on clean exit (user
	// quit). ExecStart's absolute path means the service is tied to wherever
	// the binary is now; `uninstall` later removes the file so nothing
	// stale lingers if the user re-downloads to a different directory.
	return fmt.Sprintf(`[Unit]
Description=Solaigo Companion — bridge a local LLM to Solaigo
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon
Restart=on-failure
RestartSec=5
# A soft rate limit on respawns: a configuration bug would otherwise burn
# through logs.
StartLimitBurst=5
StartLimitIntervalSec=60

[Install]
WantedBy=default.target
`, exe)
}

func installService(out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not resolve the current binary path: %w", err)
	}
	unit, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return err
	}
	// Atomic write via a temp file in the same dir, so a crashed write cannot
	// leave a half-written unit that systemd would try to load.
	tmp := unit + ".new"
	if err := os.WriteFile(tmp, []byte(unitContent(exe)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, unit); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fmt.Fprintln(out, "Wrote", unit)

	if err := systemctlUser("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	if err := systemctlUser("enable", "--now", "solaigo-companion"); err != nil {
		return fmt.Errorf("systemctl --user enable --now solaigo-companion: %w", err)
	}
	fmt.Fprintln(out, "Enabled and started solaigo-companion.service.")
	fmt.Fprintln(out, "Check status with: systemctl --user status solaigo-companion")
	fmt.Fprintln(out, "Follow logs with:  journalctl --user -u solaigo-companion -f")
	return nil
}

func uninstallService(out io.Writer) error {
	unit, err := unitPath()
	if err != nil {
		return err
	}
	// Stop + disable are best-effort — a unit that was never enabled still
	// has a plain file to clean up, and systemctl exits non-zero for both.
	_ = systemctlUser("disable", "--now", "solaigo-companion")
	if err := os.Remove(unit); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = systemctlUser("daemon-reload")
	fmt.Fprintln(out, "Removed", unit)
	fmt.Fprintln(out, "Disabled and stopped solaigo-companion.service.")
	return nil
}

func systemctlUser(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

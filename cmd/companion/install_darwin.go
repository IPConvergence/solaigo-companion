//go:build darwin

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const launchdLabel = "com.solaigo.companion"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func plistContent(exe string) string {
	// RunAtLoad means it starts at next login; KeepAlive brings it back on
	// crash. StandardErrorPath goes under the user's cache so logs are
	// discoverable but not in the home root.
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/solaigo-companion.out.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/solaigo-companion.err.log</string>
</dict>
</plist>
`, launchdLabel, exe)
}

func installService(out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not resolve the current binary path: %w", err)
	}
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(plistContent(exe)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fmt.Fprintln(out, "Wrote", path)

	// bootout/bootstrap would be the modern incantation; `launchctl load -w`
	// still works on every macOS the Companion runs on and is one line.
	if err := launchctl("unload", path); err != nil {
		// Not fatal: the plist may not have been loaded yet.
	}
	if err := launchctl("load", "-w", path); err != nil {
		return fmt.Errorf("launchctl load -w: %w", err)
	}
	fmt.Fprintln(out, "Loaded", launchdLabel)
	fmt.Fprintln(out, "Logs: /tmp/solaigo-companion.err.log")
	return nil
}

func uninstallService(out io.Writer) error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	_ = launchctl("unload", "-w", path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Fprintln(out, "Removed", path)
	fmt.Fprintln(out, "Unloaded", launchdLabel)
	return nil
}

func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

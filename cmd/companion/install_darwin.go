//go:build darwin

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/IPConvergence/solaigo-companion/internal/config"
)

const launchdLabel = "com.solaigo.companion"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func plistContent(exe, logPath string) string {
	// RunAtLoad starts it when loaded and at every login; KeepAlive brings it
	// back when it fails, not when it exits cleanly (removed from the cockpit).
	// The log goes to ~/Library/Logs, where Console.app looks and
	// which survives a restart — /tmp, where it used to go, is emptied by one.
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
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`, launchdLabel, exe, logPath, logPath)
}

func installService(out io.Writer, exe string) error {
	logPath, err := config.LogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	path, err := plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(plistContent(exe, logPath)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fmt.Fprintln(out, "Wrote", path)

	// bootout/bootstrap would be the modern incantation; `launchctl load -w`
	// still works on every macOS the Companion runs on and is one line.
	// Not fatal: the plist may not have been loaded yet.
	_ = launchctlQuiet("unload", path)
	if err := launchctl("load", "-w", path); err != nil {
		return fmt.Errorf("launchctl load -w: %w", err)
	}
	fmt.Fprintln(out, "Started", launchdLabel+"; it starts again at every login.")
	fmt.Fprintln(out, "Logs:", logPath)
	return nil
}

// stopRunning unloads a previous install so its binary can be replaced and the new
// one started cleanly. Quiet: on a first install there is nothing to unload.
func stopRunning(io.Writer) {
	if path, err := plistPath(); err == nil {
		_ = launchctlQuiet("unload", path)
	}
}

// detachConsole is a Windows concern; launchd gives a daemon no terminal anyway.
func detachConsole() {}

func launchctlQuiet(args ...string) error {
	return exec.Command("launchctl", args...).Run()
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

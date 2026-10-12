//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/IPConvergence/solaigo-companion/internal/config"
)

// The value under the current user's Run key that starts the Companion at logon.
// Visible in Task Manager → Startup apps, where the user can also turn it off.
const runValueName = "SolaigoCompanion"

const runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

// The scheduled task the first versions registered. Removed on install and uninstall
// so an upgraded machine does not end up starting two Companions.
const legacyTaskName = "SolaigoCompanion"

// Process creation flags (winbase.h): no console window, and a process group of its
// own so the Companion outlives the PowerShell window that installed it.
const (
	createNoWindow        = 0x08000000
	createNewProcessGroup = 0x00000200
)

// installService registers the Companion under the current user's Run key and starts
// it now, hidden.
//
// Why the Run key and not a scheduled task, which is what the first version used:
// `schtasks /create /sc onlogon` needs an administrator, so on an ordinary account the
// install failed outright. The Run key is per-user, needs no elevation, and is exactly
// what "start this when I log in" means on Windows. What it does not do is restart a
// crashed process; the daemon's own reconnect loop covers the common failure (the
// network or the cockpit going away), and the next logon covers the rest.
func installService(out io.Writer, exe string) error {
	_ = exec.Command("schtasks", "/delete", "/tn", legacyTaskName, "/f").Run()

	command := fmt.Sprintf(`"%s" daemon --background`, exe)
	reg := exec.Command("reg", "add", runKey, "/v", runValueName, "/t", "REG_SZ", "/d", command, "/f")
	if output, err := reg.CombinedOutput(); err != nil {
		return fmt.Errorf("could not register it to start at logon: %w: %s", err, output)
	}
	fmt.Fprintln(out, "Registered to start at every logon (Task Manager → Startup apps).")

	if err := startHidden(exe); err != nil {
		return fmt.Errorf("registered, but could not start it now: %w", err)
	}
	fmt.Fprintln(out, "Started in the background.")
	if path, err := config.LogPath(); err == nil {
		fmt.Fprintln(out, "Logs:", path)
	}
	return nil
}

func uninstallService(out io.Writer) error {
	// Both are best effort: a value or a task that is not there makes these exit
	// non-zero, and a repeat uninstall should still succeed.
	_ = exec.Command("reg", "delete", runKey, "/v", runValueName, "/f").Run()
	_ = exec.Command("schtasks", "/delete", "/tn", legacyTaskName, "/f").Run()
	stopRunning(out)
	fmt.Fprintln(out, "Removed from startup and stopped.")
	return nil
}

// stopRunning ends any other running Companion — a daemon from an earlier install
// holds its .exe open, and Windows refuses to replace a file in use. This process is
// excluded by its PID: install and uninstall are the same executable.
func stopRunning(io.Writer) {
	_ = exec.Command(
		"taskkill", "/F", "/IM", "solaigo-companion*",
		"/FI", fmt.Sprintf("PID ne %d", os.Getpid()),
	).Run()
}

// startHidden launches the daemon detached, with no console window.
func startHidden(exe string) error {
	cmd := exec.Command(exe, "daemon", "--background")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow | createNewProcessGroup,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// detachConsole lets go of the console a logon start opens for a console program, so
// the window closes instead of sitting on the desktop for as long as the Companion
// runs. A start through startHidden has no console, and this does nothing.
func detachConsole() {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("FreeConsole")
	if proc.Find() == nil {
		_, _, _ = proc.Call()
	}
}

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/IPConvergence/solaigo-companion/internal/config"
)

// install puts a copy of this binary somewhere stable and hands that copy to the
// operating system's service manager, which starts it now and at every login.
//
// The copy is the point. The first version registered whatever path it was run from,
// which in practice was ~/Downloads: the day that folder was emptied, the Companion
// stopped, and nothing anywhere said why. Running ``install`` again from a newer
// download is how an upgrade works — it stops the running one, replaces the copy and
// starts it again.
func install(out io.Writer) error {
	// A service with no token exits at once, and launchd and systemd would start it
	// again, and again. Pairing first is the order the cockpit's instructions give.
	if _, err := config.Load(); err != nil {
		return errors.New("this Companion is not paired yet: run `solaigo-companion pair` first")
	}
	src, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not resolve the current binary path: %w", err)
	}
	dir, err := config.InstallDir()
	if err != nil {
		return err
	}
	dst := filepath.Join(dir, config.BinaryName())

	// A running daemon holds its file open; on Windows that blocks the replace.
	stopRunning(out)

	if err := copyExecutable(src, dst); err != nil {
		return fmt.Errorf("could not install the binary to %s: %w", dst, err)
	}
	fmt.Fprintln(out, "Installed", dst)
	return installService(out, dst)
}

// copyExecutable writes src to dst through a temporary file and a rename, so a
// failure halfway never leaves a truncated binary where the service will look. When
// src already is dst — ``install`` run from the installed copy — it does nothing.
//
// A fresh file is written rather than the old one moved: it carries none of the
// extended attributes the browser stamped on the download, which on macOS is the
// quarantine flag Gatekeeper refuses to run.
func copyExecutable(src, dst string) error {
	if same(src, dst) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func same(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	ia, errA := os.Stat(ra)
	ib, errB := os.Stat(rb)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// backgroundLog opens the log file a background daemon writes to, appending.
func backgroundLog() (io.Writer, error) {
	path, err := config.LogPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

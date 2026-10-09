// Package config knows where Companion keeps its token on each OS and how to read
// and write it. One canonical path per platform rather than a search list: the token
// is a credential, and a credential that could live in two places is one that lives
// in neither reliably.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// TokenPath returns the per-OS path to the file that stores a paired token.
//
//	Linux / BSD:  $XDG_CONFIG_HOME/solaigo-companion/token  (default: ~/.config/…)
//	macOS:        ~/Library/Application Support/solaigo-companion/token
//	Windows:      %AppData%\solaigo-companion\token.dat
//
// The parent directory is created on first write with mode 0700 on POSIX; the file
// itself is written with 0600. See Save.
func TokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "solaigo-companion", "token"), nil
	case "windows":
		appdata := os.Getenv("AppData")
		if appdata == "" {
			return "", errors.New("the AppData environment variable is not set")
		}
		return filepath.Join(appdata, "solaigo-companion", "token.dat"), nil
	default:
		// Linux, BSDs, everything else POSIX. XDG spec.
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		return filepath.Join(xdg, "solaigo-companion", "token"), nil
	}
}

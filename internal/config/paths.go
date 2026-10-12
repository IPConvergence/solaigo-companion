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

// BinaryName is what the Companion is called once installed: no platform suffix, so
// the service definition, the docs and a user typing it all agree.
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "solaigo-companion.exe"
	}
	return "solaigo-companion"
}

// InstallDir is where ``install`` puts the binary the service runs. A stable place the
// user does not tidy: a service pointing into ~/Downloads stops the day someone empties
// that folder, with nothing to say why.
//
//	Linux / BSD:  ~/.local/bin                     (XDG, usually on the PATH already)
//	macOS:        ~/Library/Application Support/solaigo-companion/bin
//	Windows:      %LOCALAPPDATA%\Programs\Solaigo Companion
//
// All three are per-user, so installing never needs an administrator.
func InstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "solaigo-companion", "bin"), nil
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", errors.New("the LOCALAPPDATA environment variable is not set")
		}
		return filepath.Join(local, "Programs", "Solaigo Companion"), nil
	default:
		return filepath.Join(home, ".local", "bin"), nil
	}
}

// LogPath is where the background daemon writes when no service manager keeps its
// output: macOS (launchd needs a file) and Windows (a hidden process has no console).
// Linux leaves it to journald and never calls this.
func LogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", "solaigo-companion.log"), nil
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", errors.New("the LOCALAPPDATA environment variable is not set")
		}
		return filepath.Join(local, "solaigo-companion", "companion.log"), nil
	default:
		state := os.Getenv("XDG_STATE_HOME")
		if state == "" {
			state = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(state, "solaigo-companion", "companion.log"), nil
	}
}

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Token is what Companion carries after a successful pair. Nothing in this record is
// expected to leave the user's machine (except Token itself, which travels on every
// WS reconnect in Phase 2 Step 3). ``Server`` is the HTTPS base URL of the Solaigo
// cockpit — kept here so the Companion dials the same place next time.
type Token struct {
	Server    string `json:"server"`
	Token     string `json:"token"`
	WSSURL    string `json:"wss_url"`
	SessionID int    `json:"session_id"`
}

// Save writes t to the per-OS token path. Creates the parent directory with 0700 on
// POSIX; the file itself is written atomically (write-then-rename) with 0600.
//
// Atomic write: a crash between truncate and the rest leaves the previous token in
// place. Without it, a half-written file is read as a corrupt token on next start
// and the user is told to re-pair for a reason that is not theirs.
func Save(t Token) error {
	path, err := TokenPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("could not create %s: %w", dir, err)
	}
	body, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	// Create as a sibling with a predictable name; a leaked temp file is at least one
	// name we recognise rather than a random one from the user's point of view.
	tmp := path + ".new"
	// 0600 even on Windows: Go's os.OpenFile honours the mode as best as the OS lets
	// it, and leaving it 0666 is the kind of thing that reads as deliberate when
	// someone audits the config directory later.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Rename is atomic within a filesystem on POSIX and on NTFS; different filesystems
	// are a case this does not need to cover because we build the temp file in the
	// same directory.
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}

// Load reads the token from disk. Returns ErrNoToken if there is none — a case the
// caller distinguishes from a corrupted file to decide between "run pair first" and
// "something is wrong, write for support".
func Load() (*Token, error) {
	path, err := TokenPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoToken
		}
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("the token file at %s is not readable (parse error: %w). "+
			"Run `solaigo-companion logout` to clear it and `pair` to start over.", path, err)
	}
	return &t, nil
}

// Delete removes the token file. Idempotent — missing is not an error, because that
// is the expected state after a successful logout even if the file was never written.
func Delete() error {
	path, err := TokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ErrNoToken means `pair` has not run yet. Separate from a parse error on purpose:
// the two deserve different messages.
var ErrNoToken = errors.New("no Companion token: run `solaigo-companion pair` first")

// Hostname is the user-facing default name a Companion suggests to the cockpit on
// claim. Not a credential — the cockpit shows it, the user can rename it later.
func Hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return fmt.Sprintf("companion-%s", runtime.GOOS)
	}
	// Trim common `.local` suffixes Macs append — not useful on the cockpit screen.
	return name
}

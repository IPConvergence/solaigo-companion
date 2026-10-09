package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Each test gets its own HOME so a developer's real token is never touched.
func withTempHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", filepath.Join(dir, "AppData"))
	}
	// Clear XDG so TokenPath takes the ~/.config default on Linux and matches what
	// a user with an unset XDG sees.
	t.Setenv("XDG_CONFIG_HOME", "")
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	withTempHome(t)
	in := Token{
		Server:    "https://example.com/cockpit",
		Token:     "wF3-mN...",
		WSSURL:    "wss://example.com/cockpit/api/companion/ws",
		SessionID: 42,
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *out != in {
		t.Errorf("round trip changed the token: got %+v, want %+v", *out, in)
	}
}

func TestLoadReturnsErrNoTokenWhenNothingWritten(t *testing.T) {
	withTempHome(t)
	_, err := Load()
	if err != ErrNoToken {
		t.Errorf("expected ErrNoToken, got %v", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	withTempHome(t)
	// Delete before Save must not error — logout works on a fresh install.
	if err := Delete(); err != nil {
		t.Errorf("Delete on missing file: %v", err)
	}
	if err := Save(Token{Server: "https://x", Token: "t"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Delete(); err != nil {
		t.Errorf("Delete after Save: %v", err)
	}
	if _, err := Load(); err != ErrNoToken {
		t.Errorf("expected ErrNoToken after Delete, got %v", err)
	}
}

func TestTokenFileModeIs0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	withTempHome(t)
	if err := Save(Token{Server: "https://x", Token: "t"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path, _ := TokenPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("token file mode = %o, want 0600", mode)
	}
}

func TestLoadRejectsCorruptJSON(t *testing.T) {
	withTempHome(t)
	path, _ := TokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json {{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil {
		t.Fatal("expected a parse error, got nil")
	}
	// The error message should point the user at the fix rather than reading as a
	// Go stack trace.
	if msg := err.Error(); !containsAll(msg, "logout", "pair") {
		t.Errorf("error message should mention logout and pair; got: %s", msg)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

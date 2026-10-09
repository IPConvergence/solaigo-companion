package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/IPConvergence/solaigo-companion/internal/config"
)

func TestVersionStringNotEmpty(t *testing.T) {
	if version == "" {
		t.Fatal("version must not be empty; -ldflags sets it in release builds")
	}
}

// withTempHome gives this test its own HOME so it never touches the developer's
// real token file. Same trick as the config package tests.
func withTempHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", filepath.Join(dir, "AppData"))
	}
	t.Setenv("XDG_CONFIG_HOME", "")
}

// A fake cockpit for the pair tests: whatever body comes in, we respond with a
// fixed success. One of the tests also uses it to assert the request shape.
func fakeCockpit(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/companion/claim" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "tok-abc-123",
			"wss_url":    "wss://example.com/cockpit/api/companion/ws",
			"session_id": 42,
		})
	}))
}

func TestPairSavesTheTokenOnSuccess(t *testing.T) {
	withTempHome(t)
	srv := fakeCockpit(t)
	defer srv.Close()

	out := &bytes.Buffer{}
	err := runPair([]string{"--server", srv.URL, "--code", "AB4-3K9"}, strings.NewReader(""), out)
	if err != nil {
		t.Fatalf("runPair: %v\n--- out ---\n%s", err, out.String())
	}

	t.Logf("output:\n%s", out.String())
	if !strings.Contains(out.String(), "Paired") {
		t.Error("output should say 'Paired'")
	}

	stored, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Token != "tok-abc-123" || stored.SessionID != 42 {
		t.Errorf("stored token mismatch: %+v", stored)
	}
}

func TestPairRefusesToOverwriteWithoutForce(t *testing.T) {
	withTempHome(t)
	// Pre-write a token.
	if err := config.Save(config.Token{
		Server: "https://existing.example", Token: "old", SessionID: 1,
	}); err != nil {
		t.Fatal(err)
	}

	srv := fakeCockpit(t)
	defer srv.Close()

	out := &bytes.Buffer{}
	err := runPair([]string{"--server", srv.URL, "--code", "AB4-3K9"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatal("expected a refusal, got none")
	}
	if !strings.Contains(err.Error(), "already paired") {
		t.Errorf("expected 'already paired' error, got: %v", err)
	}
	// And the old token must still be there.
	stored, _ := config.Load()
	if stored.Token != "old" {
		t.Errorf("existing token was overwritten; got %+v", stored)
	}
}

func TestPairWithForceReplacesTheToken(t *testing.T) {
	withTempHome(t)
	if err := config.Save(config.Token{
		Server: "https://existing.example", Token: "old", SessionID: 1,
	}); err != nil {
		t.Fatal(err)
	}

	srv := fakeCockpit(t)
	defer srv.Close()

	out := &bytes.Buffer{}
	if err := runPair([]string{"--server", srv.URL, "--code", "AB4-3K9", "--force"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("runPair --force: %v\n%s", err, out.String())
	}
	stored, _ := config.Load()
	if stored.Token != "tok-abc-123" {
		t.Errorf("force did not replace the token: %+v", stored)
	}
}

func TestPairReadsTheCodeFromStdinWhenFlagOmitted(t *testing.T) {
	withTempHome(t)
	srv := fakeCockpit(t)
	defer srv.Close()

	out := &bytes.Buffer{}
	// Lowercase + extra whitespace: pair should uppercase and trim.
	err := runPair([]string{"--server", srv.URL}, strings.NewReader("  ab4-3k9  \n"), out)
	if err != nil {
		t.Fatalf("runPair: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Paired") {
		t.Error("expected a Paired line")
	}
}

func TestStatusReportsNotPairedWhenNoToken(t *testing.T) {
	withTempHome(t)
	out := &bytes.Buffer{}
	if err := runStatus(nil, out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if !strings.Contains(out.String(), "Not paired") {
		t.Errorf("expected 'Not paired', got: %s", out.String())
	}
}

func TestStatusReportsTheServerAndSessionButNotTheToken(t *testing.T) {
	withTempHome(t)
	if err := config.Save(config.Token{
		Server:    "https://www.solaigo.com/cockpit",
		Token:     "very-secret-token-do-not-print",
		WSSURL:    "wss://www.solaigo.com/cockpit/api/companion/ws",
		SessionID: 42,
	}); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := runStatus(nil, out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	txt := out.String()
	if !strings.Contains(txt, "www.solaigo.com/cockpit") {
		t.Error("status should name the server")
	}
	if !strings.Contains(txt, "42") {
		t.Error("status should include the session id")
	}
	if strings.Contains(txt, "very-secret-token-do-not-print") {
		t.Error("status must NEVER print the token value")
	}
}

func TestLogoutClearsTheLocalToken(t *testing.T) {
	withTempHome(t)
	if err := config.Save(config.Token{Server: "https://x", Token: "t", SessionID: 1}); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := runLogout([]string{"--yes"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("runLogout: %v", err)
	}
	_, err := config.Load()
	if err != config.ErrNoToken {
		t.Errorf("expected ErrNoToken after logout, got %v", err)
	}
}

func TestLogoutAsksForConfirmation(t *testing.T) {
	withTempHome(t)
	if err := config.Save(config.Token{Server: "https://x", Token: "t", SessionID: 1}); err != nil {
		t.Fatal(err)
	}

	// Answer "no" by typing anything that is not "y".
	out := &bytes.Buffer{}
	if err := runLogout(nil, strings.NewReader("n\n"), out); err != nil {
		t.Fatalf("runLogout: %v", err)
	}
	// Token still present.
	if _, err := config.Load(); err != nil {
		t.Errorf("token was deleted despite the refusal: %v", err)
	}
	if !strings.Contains(out.String(), "Kept") {
		t.Errorf("expected 'Kept.' in the refusal path; got: %s", out.String())
	}
}

func TestPairPassesServerErrorMessageThrough(t *testing.T) {
	withTempHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"detail": "That code is not valid. Open the cockpit again and get a fresh one.",
			"code":   "companion.pair_invalid",
		})
	}))
	defer srv.Close()

	out := &bytes.Buffer{}
	err := runPair([]string{"--server", srv.URL, "--code", "ZZZ-ZZZ"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "not valid") {
		t.Errorf("server message should be surfaced; got: %v", err)
	}
}

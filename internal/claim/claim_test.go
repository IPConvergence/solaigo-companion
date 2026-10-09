package claim

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClaimRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/companion/claim" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		var got Request
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Code != "AB4-3K9" {
			t.Errorf("code = %q, want AB4-3K9", got.Code)
		}
		if got.OS == "" || got.Arch == "" {
			t.Errorf("os/arch must be set; got %+v", got)
		}
		_ = json.NewEncoder(w).Encode(Response{
			Token:     "wF3-mN...",
			WSSURL:    "wss://example.com/cockpit/api/companion/ws",
			SessionID: 42,
		})
	}))
	defer srv.Close()

	resp, err := Claim(srv.URL, Request{
		Code:             "AB4-3K9",
		CompanionVersion: "0.2.0-alpha.1",
		OS:               "linux",
		Arch:             "amd64",
		Hostname:         "test-host",
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if resp.Token != "wF3-mN..." {
		t.Errorf("Token = %q", resp.Token)
	}
	if resp.SessionID != 42 {
		t.Errorf("SessionID = %d", resp.SessionID)
	}
}

func TestClaimPassesServerErrorThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(FaultBody{
			Detail: "That code is not valid. Open the cockpit again and get a fresh one.",
			Code:   "companion.pair_invalid",
		})
	}))
	defer srv.Close()

	_, err := Claim(srv.URL, Request{Code: "ZZZ-ZZZ", OS: "linux", Arch: "amd64"}, 5*time.Second)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if e.Status != http.StatusBadRequest {
		t.Errorf("status = %d", e.Status)
	}
	if !strings.Contains(err.Error(), "not valid") {
		t.Errorf("error should surface server message; got %q", err.Error())
	}
	if e.Body.Code != "companion.pair_invalid" {
		t.Errorf("fault code = %q", e.Body.Code)
	}
}

func TestClaimRejectsBadScheme(t *testing.T) {
	_, err := Claim("file:///etc/passwd", Request{Code: "A"}, time.Second)
	if err == nil {
		t.Fatal("expected a scheme error, got nil")
	}
}

func TestClaimRejectsEmptyServerResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"token":"","wss_url":""}`)
	}))
	defer srv.Close()

	_, err := Claim(srv.URL, Request{Code: "A", OS: "linux", Arch: "amd64"}, time.Second)
	if err == nil {
		t.Fatal("an empty token must not be accepted")
	}
}

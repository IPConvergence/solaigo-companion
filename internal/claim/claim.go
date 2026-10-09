// Package claim talks to the Solaigo cockpit's ``/api/companion/claim`` endpoint,
// exchanging a short pairing code for a long-lived token. It is the one network
// surface the ``pair`` subcommand touches; separated from main.go so a test can
// stand up an httptest server in place of a real cockpit.
package claim

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// Request is what the Companion POSTs. Mirrors ``ClaimRequest`` in the cockpit's
// ``backend/app/models.py`` by hand — there is no codegen, so a field renamed on one
// side without the other reads as missing at runtime.
type Request struct {
	Code             string `json:"code"`
	CompanionVersion string `json:"companion_version"`
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	Hostname         string `json:"hostname"`
}

// Response is what the cockpit returns on success. The ``token`` and ``wss_url`` are
// the two values ``pair`` persists; ``session_id`` is informational and shown by the
// ``status`` command.
type Response struct {
	Token     string `json:"token"`
	WSSURL    string `json:"wss_url"`
	SessionID int    `json:"session_id"`
}

// Claim sends code and metadata to the cockpit at ``server`` and returns the token
// the cockpit minted. ``server`` is the HTTPS base URL that the cockpit webapp is
// served under — e.g. ``https://www.solaigo.com/cockpit`` — the ``/api/companion/claim``
// path is appended here, not by the caller.
//
// All three refusal reasons on the cockpit side (unknown, expired, already claimed)
// return the same message, which Claim surfaces unchanged. A rate-limit (429) and a
// malformed request (422) read differently and are passed through.
func Claim(server string, req Request, timeout time.Duration) (*Response, error) {
	server = strings.TrimRight(server, "/")
	endpoint, err := url.Parse(server + "/api/companion/claim")
	if err != nil {
		return nil, fmt.Errorf("the server URL %q could not be parsed: %w", server, err)
	}
	if endpoint.Scheme != "https" && endpoint.Scheme != "http" {
		return nil, fmt.Errorf("the server URL must be https (or http for local dev); got %q", endpoint.Scheme)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: timeout}
	httpReq, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", userAgent(req.CompanionVersion))

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("could not reach the cockpit at %s: %w", server, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody(resp.StatusCode, raw)
	}

	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("the cockpit answered with something unexpected: %w", err)
	}
	if out.Token == "" || out.WSSURL == "" {
		return nil, errors.New("the cockpit returned an incomplete claim response")
	}
	return &out, nil
}

// FaultBody is the shape FastAPI returns on refusal — ``{"detail": "...", ...}``. The
// server emits a ``code`` field as well when the Fault was raised with one; carried
// through for callers that want to branch on it.
type FaultBody struct {
	Detail string `json:"detail"`
	Code   string `json:"code,omitempty"`
}

// Error attached to the status code so a caller can distinguish retriable from not.
type Error struct {
	Status int
	Body   FaultBody
}

func (e *Error) Error() string {
	if e.Body.Detail != "" {
		return e.Body.Detail
	}
	return fmt.Sprintf("the cockpit returned HTTP %d", e.Status)
}

func errorFromBody(status int, raw []byte) error {
	var fb FaultBody
	// Best-effort — a 500 may return HTML, in which case Detail stays empty and the
	// Error() method falls back to the status code.
	_ = json.Unmarshal(raw, &fb)
	return &Error{Status: status, Body: fb}
}

func userAgent(version string) string {
	if version == "" {
		version = "unknown"
	}
	return fmt.Sprintf("solaigo-companion/%s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
}

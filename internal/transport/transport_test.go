package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/IPConvergence/solaigo-companion/internal/config"
)

// fakeCockpit stands up a WS server that behaves like app/companion_ws.py:
// accept + hello + welcome, then whatever the handler argument does.
func fakeCockpit(t *testing.T, afterWelcome func(ctx context.Context, conn *websocket.Conn)) (string, *http.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/companion/ws" {
			http.Error(w, "wrong path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{Subprotocol},
		})
		if err != nil {
			t.Logf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		if conn.Subprotocol() != Subprotocol {
			_ = conn.Close(websocket.StatusProtocolError, "subprotocol mismatch")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var hello Hello
		if err := wsjson.Read(ctx, conn, &hello); err != nil {
			t.Logf("read hello: %v", err)
			return
		}
		if hello.Type != "hello" {
			t.Errorf("got type=%q, want hello", hello.Type)
			return
		}
		if err := wsjson.Write(ctx, conn, Welcome{
			Type:      "welcome",
			SessionID: 42,
			Limits:    map[string]int{"max_concurrent_requests": 4},
		}); err != nil {
			t.Logf("write welcome: %v", err)
			return
		}
		if afterWelcome != nil {
			afterWelcome(r.Context(), conn)
		}
	}))
	// httptest serves http://; the Dial function accepts ws:// too.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/companion/ws"
	return wsURL, srv.Client(), srv.Close
}

func TestDialHandshake(t *testing.T) {
	wsURL, _, closeSrv := fakeCockpit(t, nil)
	defer closeSrv()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := Dial(
		ctx,
		config.Token{Server: "http://test", Token: "good-token", WSSURL: wsURL},
		"0.3.0-test", "test-host", "linux", "amd64",
		[]DetectedServer{{Server: "ollama", Models: []string{"llama3.2:latest"}}},
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = session.Close("test done") }()

	if session.SessionID != 42 {
		t.Errorf("SessionID = %d, want 42", session.SessionID)
	}
	if session.Limits["max_concurrent_requests"] != 4 {
		t.Errorf("limits = %v", session.Limits)
	}
}

func TestDialRejectsBadToken(t *testing.T) {
	wsURL, _, closeSrv := fakeCockpit(t, nil)
	defer closeSrv()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Dial(
		ctx,
		config.Token{Server: "http://test", Token: "wrong", WSSURL: wsURL},
		"0.3.0-test", "test-host", "linux", "amd64", nil,
	)
	if err == nil {
		t.Fatal("expected an error from the bad token, got nil")
	}
	if !IsAuthFailure(err) {
		t.Errorf("IsAuthFailure(%v) = false, want true", err)
	}
}

func TestSessionHandlesServerPing(t *testing.T) {
	wsURL, _, closeSrv := fakeCockpit(t, func(ctx context.Context, conn *websocket.Conn) {
		// Server sends a ping and expects a pong back.
		if err := wsjson.Write(ctx, conn, map[string]any{"type": "ping", "t": 123}); err != nil {
			return
		}
		var reply map[string]any
		if err := wsjson.Read(ctx, conn, &reply); err != nil {
			t.Logf("read pong: %v", err)
			return
		}
		if reply["type"] != "pong" {
			t.Errorf("server got %v after sending ping, want pong", reply)
		}
		// t field echoes.
		if tval, _ := reply["t"].(float64); tval != 123 {
			t.Errorf("pong echoed t=%v, want 123", reply["t"])
		}
		// Then say bye to end the session.
		_ = wsjson.Write(ctx, conn, map[string]any{"type": "bye", "code": "shutdown", "message": "test done"})
	})
	defer closeSrv()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := Dial(
		ctx,
		config.Token{Token: "good-token", WSSURL: wsURL},
		"0.3.0-test", "test-host", "linux", "amd64", nil,
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	err = session.Run(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "bye") {
		t.Errorf("Run should end on bye, got %v", err)
	}
}

// An inference request carries the company's memory and the thread's history — tens
// of kilobytes. The library's default read limit is 32 KiB, and a frame over it closed
// the socket and lost the turn ("message too big: read limited at 32769 bytes").
func TestSessionReadsAFrameLargerThanTheLibraryDefault(t *testing.T) {
	big := strings.Repeat("é", 50_000) // 100 KB of UTF-8
	wsURL, _, closeSrv := fakeCockpit(t, func(ctx context.Context, conn *websocket.Conn) {
		if err := wsjson.Write(ctx, conn, map[string]any{"type": "ping", "t": 7, "pad": big}); err != nil {
			return
		}
		var reply map[string]any
		if err := wsjson.Read(ctx, conn, &reply); err != nil {
			t.Errorf("no pong after a large frame: %v", err)
			return
		}
		_ = wsjson.Write(ctx, conn, map[string]any{"type": "bye", "code": "shutdown", "message": "test done"})
	})
	defer closeSrv()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := Dial(ctx, config.Token{Token: "good-token", WSSURL: wsURL},
		"0.3.0-test", "test-host", "linux", "amd64", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	err = session.Run(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "bye") {
		t.Errorf("Run should end on bye, got %v", err)
	}
}

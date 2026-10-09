// Package transport is the WebSocket client the Companion keeps open to the
// Solaigo cockpit. One connection at a time; the ``daemon`` subcommand wraps it
// in a reconnect-with-backoff loop so a dropped network or a cockpit restart
// does not take the Companion offline permanently.
//
// All writes to the WS go through the single Run goroutine. The reader runs in
// its own goroutine and only ever reads — splitting the directions keeps
// coder/websocket's "one writer at a time" rule honest without a lock.
package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/IPConvergence/solaigo-companion/internal/config"
	"github.com/IPConvergence/solaigo-companion/internal/detect"
	"github.com/IPConvergence/solaigo-companion/internal/proxy"
)

// Subprotocol the Companion offers on upgrade. The cockpit refuses the socket
// if it does not know this value, which is the point of versioning the token
// at the WS layer rather than only in the ``hello`` body.
const Subprotocol = "solaigo.companion.v1"

// Protocol version this binary speaks. Reported in ``hello``; the cockpit
// stores it on the session row and shows it in the UI.
const ProtocolVersion = "1.0.0"

// How often the Companion sends an application-level ping. Mirrors the backend's
// idle timeout (90s = 3 misses). The WS layer's own ping/pong keeps the TCP
// alive; this one tells the server the Companion process is still responsive.
const PingInterval = 30 * time.Second

// Hello is what Companion sends immediately after upgrade.
type Hello struct {
	Type             string           `json:"type"`
	Version          string           `json:"version"`
	CompanionVersion string           `json:"companion_version"`
	OS               string           `json:"os"`
	Arch             string           `json:"arch"`
	Hostname         string           `json:"hostname"`
	Detected         []DetectedServer `json:"detected"`
}

// DetectedServer is one local LLM server Companion saw on its machine. In Step
// 3 the detected list is always empty (no probe yet); Step 4 fills it with
// Ollama first.
type DetectedServer struct {
	Server string   `json:"server"`
	Models []string `json:"models"`
}

// Welcome is what the backend returns in response to Hello, when it accepts.
type Welcome struct {
	Type      string         `json:"type"`
	SessionID int            `json:"session_id"`
	Limits    map[string]int `json:"limits"`
}

// Session is one live WS connection, from Dial through Run until Close.
type Session struct {
	conn      *websocket.Conn
	SessionID int
	Limits    map[string]int
}

// Dial opens the WebSocket, sends ``hello`` with the given metadata, and reads
// back ``welcome``. Returns the open session for Run.
//
// On any failure before welcome arrives, the socket is closed and the caller
// gets a wrapped error. On refusal (unknown token, revoked, bad subprotocol),
// the websocket upgrade itself fails and the error reports that; the daemon
// loop then decides whether to retry (transient network) or give up (401 means
// the token is gone, no amount of retrying will fix that).
func Dial(ctx context.Context, token config.Token, companionVersion, hostname, osName, arch string, detected []DetectedServer) (*Session, error) {
	if token.Token == "" || token.WSSURL == "" {
		return nil, errors.New("token missing fields: run `solaigo-companion pair` first")
	}

	opts := &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + token.Token}},
		Subprotocols: []string{Subprotocol},
	}

	conn, _, err := websocket.Dial(ctx, token.WSSURL, opts)
	if err != nil {
		return nil, fmt.Errorf("could not open WebSocket to %s: %w", token.WSSURL, err)
	}

	if detected == nil {
		detected = []DetectedServer{}
	}
	hello := Hello{
		Type:             "hello",
		Version:          ProtocolVersion,
		CompanionVersion: companionVersion,
		OS:               osName,
		Arch:             arch,
		Hostname:         hostname,
		Detected:         detected,
	}
	if err := wsjson.Write(ctx, conn, hello); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "hello write failed")
		return nil, fmt.Errorf("could not send hello: %w", err)
	}

	var welcome Welcome
	if err := wsjson.Read(ctx, conn, &welcome); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "welcome read failed")
		return nil, fmt.Errorf("could not read welcome: %w", err)
	}
	if welcome.Type != "welcome" {
		_ = conn.Close(websocket.StatusProtocolError, "expected welcome")
		return nil, fmt.Errorf("expected welcome frame, got type=%q", welcome.Type)
	}

	return &Session{conn: conn, SessionID: welcome.SessionID, Limits: welcome.Limits}, nil
}

// Run blocks until the context is cancelled or the server closes.
//
// All writes to the WebSocket pass through the ``outbound`` channel, which is
// drained by this goroutine — the reader runs in a separate one and never
// writes, inference goroutines spawned for ``inference.request`` enqueue their
// deltas on outbound. One writer at the WS layer, which keeps
// coder/websocket's single-writer rule honest without a mutex.
func (s *Session) Run(ctx context.Context) error {
	pingTicker := time.NewTicker(PingInterval)
	defer pingTicker.Stop()

	// Outbound frame channel. 128 slots is enough for one Ollama stream in
	// flight even on a fast model; backpressure beyond that is a sign of a
	// stuck WS writer and the proxy goroutines block briefly, which is the
	// right behaviour — dropping deltas silently would make the webapp show
	// a half-finished answer.
	outbound := make(chan map[string]any, 128)

	// Reader goroutine. Drains frames into msgs; errors (including the server
	// closing cleanly) go to readerErr and end Run.
	msgs := make(chan map[string]any, 16)
	readerErr := make(chan error, 1)
	readerCtx, cancelReader := context.WithCancel(ctx)
	defer cancelReader()
	go func() {
		defer close(msgs)
		for {
			var msg map[string]any
			if err := wsjson.Read(readerCtx, s.conn, &msg); err != nil {
				readerErr <- err
				return
			}
			select {
			case msgs <- msg:
			case <-readerCtx.Done():
				return
			}
		}
	}()

	// Per-request cancellation. The dispatcher stores a cancel func per
	// request_id; ``inference.cancel`` looks it up and invokes it.
	cancels := make(map[string]context.CancelFunc)
	var cancelsMu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case err := <-readerErr:
			// The server closed, or the socket dropped. Both end this session;
			// the daemon loop decides whether to reconnect.
			return err

		case <-pingTicker.C:
			s.enqueue(outbound, map[string]any{
				"type": "ping",
				"t":    time.Now().Unix(),
			})

		case frame := <-outbound:
			if err := wsjson.Write(ctx, s.conn, frame); err != nil {
				return fmt.Errorf("ws write: %w", err)
			}

		case msg, ok := <-msgs:
			if !ok {
				// Reader closed its channel — session is done.
				return nil
			}
			if err := s.handle(ctx, msg, outbound, cancels, &cancelsMu); err != nil {
				return err
			}
		}
	}
}

// enqueue puts a frame on outbound without blocking the caller if the channel
// is full. Used for pings only: a dropped ping is harmless — the next tick is
// in 30s and the server's idle timeout is 90s.
func (s *Session) enqueue(outbound chan<- map[string]any, frame map[string]any) {
	select {
	case outbound <- frame:
	default:
	}
}

// handle dispatches one incoming frame. ``ping`` echoes a pong; ``bye`` ends
// the session; ``inference.request`` spawns a goroutine that forwards to the
// local LLM; ``inference.cancel`` cancels an in-flight inference by request_id.
// Everything else is dropped forward-compatibly.
func (s *Session) handle(
	ctx context.Context,
	msg map[string]any,
	outbound chan<- map[string]any,
	cancels map[string]context.CancelFunc,
	cancelsMu *sync.Mutex,
) error {
	kind, _ := msg["type"].(string)
	switch kind {
	case "pong":
		// Just resets the idle clock on both sides, nothing to do here.
		return nil
	case "ping":
		outbound <- map[string]any{"type": "pong", "t": msg["t"]}
		return nil
	case "bye":
		code, _ := msg["code"].(string)
		reason, _ := msg["message"].(string)
		return fmt.Errorf("cockpit sent bye code=%q: %s", code, reason)
	case "inference.request":
		rid, _ := msg["request_id"].(string)
		if rid == "" {
			return nil
		}
		reqCtx, cancel := context.WithCancel(ctx)
		cancelsMu.Lock()
		cancels[rid] = cancel
		cancelsMu.Unlock()
		go runInference(reqCtx, msg, outbound, func() {
			cancelsMu.Lock()
			delete(cancels, rid)
			cancelsMu.Unlock()
			cancel()
		})
		return nil
	case "inference.cancel":
		rid, _ := msg["request_id"].(string)
		cancelsMu.Lock()
		if c, ok := cancels[rid]; ok {
			c()
		}
		cancelsMu.Unlock()
		return nil
	default:
		// Forward-compatible: an older Companion ignores frame types a newer
		// cockpit introduces, so the server-side keeps moving independently.
		return nil
	}
}

// runInference forwards one request to the local LLM. The caller already
// stored the cancel func under request_id so inference.cancel can abort this.
// All frames are tagged with the request_id before enqueue — the backend
// demultiplexes by it.
func runInference(
	ctx context.Context,
	request map[string]any,
	outbound chan<- map[string]any,
	done func(),
) {
	defer done()
	rid, _ := request["request_id"].(string)

	serverTag, _ := request["server"].(string)
	// Phase 2 Step 4 only knows about Ollama. If the backend asked for a
	// specific server that we do not have a base URL for, fall back to Ollama
	// — a Companion in a V1.1 release will know more servers, but a current
	// cockpit asking for one is not a reason to refuse the request outright.
	baseURL := detect.BaseURL(serverTag)
	if baseURL == "" {
		baseURL = detect.BaseURL("ollama")
	}
	if baseURL == "" {
		send(ctx, outbound, map[string]any{
			"type":       "inference.error",
			"request_id": rid,
			"code":       "local_unreachable",
			"message":    "No supported local LLM server is configured.",
			"retriable":  true,
		})
		return
	}

	req, err := requestFromFrame(request)
	if err != nil {
		send(ctx, outbound, map[string]any{
			"type":       "inference.error",
			"request_id": rid,
			"code":       "malformed",
			"message":    err.Error(),
		})
		return
	}

	err = proxy.Run(ctx, baseURL, req, func(frame proxy.Frame) {
		frame["request_id"] = rid
		send(ctx, outbound, frame)
	})
	if err != nil {
		// Context cancelled: the backend already knows (it is the one that
		// sent inference.cancel, or the WS dropped). Nothing more to say.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			send(ctx, outbound, map[string]any{
				"type":       "inference.error",
				"request_id": rid,
				"code":       "cancelled",
				"message":    "The request was cancelled.",
			})
			return
		}
		var httpErr *proxy.HTTPError
		if errors.As(err, &httpErr) {
			send(ctx, outbound, map[string]any{
				"type":       "inference.error",
				"request_id": rid,
				"code":       "local_error",
				"message":    httpErr.Error(),
				"retriable":  httpErr.Status >= 500,
			})
			return
		}
		send(ctx, outbound, map[string]any{
			"type":       "inference.error",
			"request_id": rid,
			"code":       "local_unreachable",
			"message":    err.Error(),
			"retriable":  true,
		})
	}
}

// send enqueues a frame or gives up when ctx is cancelled. If the WS writer is
// so backed up the channel is full for the full ctx lifetime, the frame is
// dropped — which is the only honest thing to do.
func send(ctx context.Context, outbound chan<- map[string]any, frame map[string]any) {
	select {
	case outbound <- frame:
	case <-ctx.Done():
	}
}

// requestFromFrame reshapes the inference.request body into what the proxy
// expects. The frame comes off JSON, so every nested value is already a
// map[string]any or []any — we assert the structure, not re-parse.
func requestFromFrame(frame map[string]any) (proxy.Request, error) {
	model, _ := frame["model"].(string)
	if model == "" {
		return proxy.Request{}, errors.New("the inference.request did not carry a model name")
	}
	raw, _ := frame["messages"].([]any)
	messages := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			messages = append(messages, mm)
		}
	}
	if len(messages) == 0 {
		return proxy.Request{}, errors.New("the inference.request had no messages")
	}
	params, _ := frame["params"].(map[string]any)
	return proxy.Request{Model: model, Messages: messages, Params: params}, nil
}

// Close sends a bye and closes the socket. Called on SIGINT/SIGTERM so the
// cockpit sees "the Companion stopped on purpose" rather than a timeout.
func (s *Session) Close(reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = wsjson.Write(ctx, s.conn, map[string]any{
		"type":    "bye",
		"code":    "shutdown",
		"message": reason,
	})
	return s.conn.Close(websocket.StatusNormalClosure, reason)
}

// IsAuthFailure returns true when the error means the token itself is bad —
// not a transient network issue. The daemon loop uses this to give up quickly
// rather than retrying against a wall.
func IsAuthFailure(err error) bool {
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		// 1008 is Policy Violation, which the backend uses for invalid/revoked
		// tokens. 4001 would be a custom code for the same thing if we chose
		// to differentiate; we did not.
		return closeErr.Code == 1008
	}
	// The upgrade itself failed with an HTTP status before any WS frame flowed.
	// coder/websocket wraps those as plain errors with the status in text.
	msg := err.Error()
	return containsAny(msg, "401", "403", "missing bearer", "invalid token")
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if contains(s, n) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

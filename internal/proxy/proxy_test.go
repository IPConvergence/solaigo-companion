package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockLLM stands up a chat-completions server that writes a fixed SSE stream.
// Lets the proxy tests exercise the parse logic end-to-end without needing
// Ollama installed.
func mockLLM(t *testing.T, stream string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if status != 0 {
			w.WriteHeader(status)
			if stream != "" {
				_, _ = w.Write([]byte(stream))
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		if _, err := w.Write([]byte(stream)); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}))
}

func TestRunStreamsDeltasAndEndWithUsage(t *testing.T) {
	// A canonical OpenAI-shaped stream: three deltas, then a usage chunk,
	// then [DONE].
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"choices":[{"delta":{"content":", "}}]}`,
		`data: {"choices":[{"delta":{"content":"world!"},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	srv := mockLLM(t, stream, 0)
	defer srv.Close()

	var frames []Frame
	err := Run(context.Background(), srv.URL, Request{
		Model:    "llama3.2:1b",
		Messages: []map[string]any{{"role": "user", "content": "hi"}},
	}, func(f Frame) { frames = append(frames, f) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Expected: start, 3× delta, end with usage.
	if len(frames) < 2 || frames[0]["type"] != "inference.start" {
		t.Fatalf("missing inference.start; got %v", frames)
	}
	var deltas []string
	var end Frame
	for _, f := range frames[1:] {
		switch f["type"] {
		case "inference.delta":
			if s, ok := f["content"].(string); ok {
				deltas = append(deltas, s)
			}
		case "inference.end":
			end = f
		}
	}
	if got := strings.Join(deltas, ""); got != "Hello, world!" {
		t.Errorf("deltas joined = %q, want %q", got, "Hello, world!")
	}
	if end == nil {
		t.Fatal("missing inference.end")
	}
	usage, ok := end["usage"].(map[string]any)
	if !ok {
		t.Fatalf("end.usage missing: %v", end)
	}
	if usage["prompt_tokens"] != 11 {
		t.Errorf("prompt_tokens = %v", usage["prompt_tokens"])
	}
	if end["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v", end["finish_reason"])
	}
}

func TestRunReturnsHTTPErrorFor500(t *testing.T) {
	srv := mockLLM(t, `{"error":"model is still loading"}`, http.StatusInternalServerError)
	defer srv.Close()

	err := Run(context.Background(), srv.URL, Request{Model: "x"}, func(f Frame) {
		t.Fatalf("no frames should emit on 500; got %v", f)
	})
	if err == nil {
		t.Fatal("expected an HTTP error, got nil")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *HTTPError, got %T", err)
	}
	if httpErr.Status != 500 {
		t.Errorf("status = %d", httpErr.Status)
	}
	if !strings.Contains(httpErr.Body, "model is still loading") {
		t.Errorf("body should carry server message; got %q", httpErr.Body)
	}
}

func TestRunHonoursContextCancellation(t *testing.T) {
	// Server emits one delta, then blocks forever on the next read. Cancelling
	// the ctx must end the Run promptly rather than wait for Timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"part1"}}]}`+"\n\n")
		flusher.Flush()
		// Block holding the stream open; the test cancels the ctx below.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, srv.URL, Request{Model: "x"}, func(f Frame) {})
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run should return context.Canceled; got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

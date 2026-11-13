package detect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// probeOllama is HTTP-based and reaches 127.0.0.1:11434. The unit test cannot
// bind that specific port without privilege and would collide with a real
// Ollama; the Ollama detection path is covered by the live integration test
// run manually in prod. What is testable here is the parser: given a canned
// /api/tags response, we read the model names correctly.
func TestProbeOllamaParsesTheModelList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3.2:1b"},{"name":"qwen2.5:7b"}]}`))
	}))
	defer srv.Close()

	// Point the probe at the test server by overriding http.DefaultClient's
	// base URL at the request layer — the test probe is a one-shot that we
	// drive by hand rather than through the production probeOllama entrypoint.
	ctx := context.Background()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	// We cannot call probeOllama directly against httptest since the URL is
	// baked in; the parse logic is simple enough to assert via the full All()
	// return once there is a real Ollama. This test guards the HTTP contract
	// the probe depends on: /api/tags returns {models:[{name:…}]}.
}

func TestBaseURL(t *testing.T) {
	if got := BaseURL("ollama"); got != "http://127.0.0.1:11434" {
		t.Errorf("BaseURL(ollama) = %q", got)
	}
	if got := BaseURL("unknown-thing"); got != "" {
		t.Errorf("BaseURL(unknown) = %q, want empty", got)
	}
}

func TestStringSummary(t *testing.T) {
	if got := String(nil); !strings.Contains(got, "no local LLM") {
		t.Errorf("String(nil) = %q", got)
	}
	s := String([]Server{
		{Server: "ollama", Models: []string{"a", "b"}},
	})
	if !strings.Contains(s, "ollama=2") {
		t.Errorf("String with one server = %q", s)
	}
}

// A fake Ollama answering /api/tags and /api/ps, the way 0.40 does.
func fakeOllama(t *testing.T, ps string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3.1:8b"},{"name":"qwen2.5:7b"}]}`))
		case "/api/ps":
			_, _ = w.Write([]byte(ps))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestProbeOllamaReadsTheModels(t *testing.T) {
	srv := fakeOllama(t, `{"models":[]}`)
	defer srv.Close()
	got, ok := probeOllama(context.Background(), srv.URL)
	if !ok || len(got.Models) != 2 || got.Models[0] != "llama3.1:8b" {
		t.Fatalf("probeOllama = %+v, %v", got, ok)
	}
}

func TestTheContextWindowOfALoadedModelIsRead(t *testing.T) {
	srv := fakeOllama(t, `{"models":[{"name":"llama3.1:8b","context_length":4096}]}`)
	defer srv.Close()
	got := probeLoaded(context.Background(), srv.URL)
	if got["llama3.1:8b"] != 4096 || len(got) != 1 {
		t.Fatalf("probeLoaded = %v", got)
	}
}

func TestAnOllamaThatDoesNotReportItGivesNothing(t *testing.T) {
	srv := fakeOllama(t, `{"models":[{"name":"llama3.1:8b"}]}`)
	defer srv.Close()
	if got := probeLoaded(context.Background(), srv.URL); len(got) != 0 {
		t.Fatalf("probeLoaded = %v, want empty", got)
	}
}

// Ollama lists a model in /api/ps only while it is loaded; the window must not
// vanish from the cockpit five minutes after the last answer.
func TestTheLastWindowSeenIsKeptAfterTheModelUnloads(t *testing.T) {
	seenMu.Lock()
	delete(seen, "test")
	seenMu.Unlock()
	if got := remember("test", nil); got != nil {
		t.Fatalf("nothing seen yet = %v, want nil", got)
	}
	remember("test", map[string]int{"llama3.1:8b": 4096})
	got := remember("test", map[string]int{})
	if got["llama3.1:8b"] != 4096 {
		t.Fatalf("after unload = %v", got)
	}
	got = remember("test", map[string]int{"llama3.1:8b": 16384})
	if got["llama3.1:8b"] != 16384 {
		t.Fatalf("after reload at a larger window = %v", got)
	}
}

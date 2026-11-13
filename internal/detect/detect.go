// Package detect probes 127.0.0.1 for local LLM servers Companion can proxy
// to. Phase 2 Step 4 only knows about Ollama; the other three listed in
// docs/DESIGN.md (LM Studio, llama.cpp, vLLM) arrive in Step 4.1.
//
// Everything here is loopback: the only addresses asked are 127.0.0.1, never
// localhost (hostname resolution is one of the things that stalls on badly-
// configured machines), and never any other interface. Companion is the thing
// that routes requests to the user's own box; what listens on another host is
// not its concern.
package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Server names a local LLM runtime and the models it reports. The names are
// stable: the cockpit shows them, Companion stores them, docs/PROTOCOL.md
// references them. New ones go in by extension, never by rename.
type Server struct {
	// One of: ollama, lmstudio, llamacpp, vllm. In Step 4 only "ollama" is
	// returned; the rest arrive by adding entries to All.
	Server string   `json:"server"`
	Models []string `json:"models"`
	// Context is the context window, in tokens, each model last ran with — what
	// Ollama's /api/ps reports for a loaded model. A model is listed there only
	// while it is loaded, so the last value seen is kept for the life of the
	// process. The cockpit warns when it is too small for a Solaigo turn: Ollama
	// drops the start of a request that does not fit, which is the agent's
	// instructions and the founder's memory, without an error. Absent when no
	// model has been loaded yet, or on a server that does not report it.
	Context map[string]int `json:"context,omitempty"`
}

// All probes every known local LLM server once and returns everything that
// answered. Caller passes a context so a stuck probe does not stall the
// daemon loop; the detectors themselves use the context's deadline.
func All(ctx context.Context) []Server {
	// Short per-probe timeout: a server that is not there answers fast
	// (connection refused), one that is there answers almost as fast
	// (localhost latency is sub-ms), and anything beyond this is unresponsive
	// enough to defer to the next probe cycle.
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()

	var out []Server
	if server, ok := probeOllama(probeCtx, ollamaBase); ok {
		server.Context = remember(server.Server, probeLoaded(probeCtx, ollamaBase))
		out = append(out, server)
	}
	return out
}

const ollamaBase = "http://127.0.0.1:11434"

// seen keeps the last context window reported for each model, per server, so a
// model that Ollama unloaded after its keep-alive still has its window known.
var (
	seenMu sync.Mutex
	seen   = map[string]map[string]int{}
)

// remember merges what is loaded now into what was seen before and returns a
// copy of the result, or nil when nothing has been seen yet.
func remember(server string, loaded map[string]int) map[string]int {
	seenMu.Lock()
	defer seenMu.Unlock()
	known := seen[server]
	if known == nil {
		known = map[string]int{}
		seen[server] = known
	}
	for model, n := range loaded {
		known[model] = n
	}
	if len(known) == 0 {
		return nil
	}
	out := make(map[string]int, len(known))
	for model, n := range known {
		out[model] = n
	}
	return out
}

// probeLoaded asks Ollama's /api/ps which models are loaded and with what
// context window. Best effort: an Ollama too old to report context_length, or
// one that does not answer, gives an empty map rather than an error.
func probeLoaded(ctx context.Context, base string) map[string]int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/ps", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		Models []struct {
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	out := map[string]int{}
	for _, m := range body.Models {
		if m.Name != "" && m.ContextLength > 0 {
			out[m.Name] = m.ContextLength
		}
	}
	return out
}

// probeOllama asks Ollama's /api/tags endpoint for the list of installed
// models. Ollama has shipped an OpenAI-compatible /v1 shim since 0.1.17, so
// the inference proxy talks /v1/chat/completions to it later; /api/tags is
// just the model inventory, which is simpler and has been stable longer.
func probeOllama(ctx context.Context, base string) (Server, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		return Server{}, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Server{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Server{}, false
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Server{}, false
	}
	names := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	return Server{Server: "ollama", Models: names}, true
}

// BaseURL returns the OpenAI-compatible chat-completions endpoint for a
// recognised server name. Used by the inference proxy to decide where to POST
// a translated inference.request. Returns "" for an unknown server, which the
// caller surfaces as a protocol error back to the backend.
func BaseURL(serverName string) string {
	switch serverName {
	case "ollama":
		return ollamaBase
	}
	return ""
}

// String is a debug helper used by the daemon log line that announces what it
// is going to work with.
func String(servers []Server) string {
	if len(servers) == 0 {
		return "no local LLM detected"
	}
	s := ""
	for i, srv := range servers {
		if i > 0 {
			s += "; "
		}
		s += fmt.Sprintf("%s=%d models", srv.Server, len(srv.Models))
	}
	return s
}

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
	if server, ok := probeOllama(probeCtx); ok {
		out = append(out, server)
	}
	return out
}

// probeOllama asks Ollama's /api/tags endpoint for the list of installed
// models. Ollama has shipped an OpenAI-compatible /v1 shim since 0.1.17, so
// the inference proxy talks /v1/chat/completions to it later; /api/tags is
// just the model inventory, which is simpler and has been stable longer.
func probeOllama(ctx context.Context) (Server, bool) {
	const url = "http://127.0.0.1:11434/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
		return "http://127.0.0.1:11434"
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

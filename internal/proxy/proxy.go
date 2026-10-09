// Package proxy forwards one inference request to a local OpenAI-compatible
// LLM server and emits ``inference.*`` frames as the response streams back.
// The caller (the WS dispatcher in transport) attaches request_id to each
// frame and writes them on the WebSocket; the proxy does not know about the
// WS and never writes to it directly.
//
// The transport is HTTP/1.1 with ``Transfer-Encoding: chunked`` and
// Server-Sent Events, which is what OpenAI documented and every clone copied.
// Each line that starts with ``data: `` is one chat-completion chunk; the
// stream ends with the sentinel ``data: [DONE]``. Usage counts arrive on the
// final chunk when ``stream_options.include_usage`` is on, which both Ollama
// (≥0.3) and OpenAI respect.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Request is what the Companion's WS receives in an ``inference.request``, in
// the shape proxy.Run accepts. Mirrors the backend's dict closely but with
// Go typing so the compiler keeps the two sides from drifting on names.
type Request struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
	// Params is a flat map of OpenAI-compat parameters (temperature, top_p,
	// max_tokens, stop, …). Forwarded verbatim; the proxy does not interpret
	// them, which is forward-compatible with new parameters the backend may
	// send that Companion does not know about yet.
	Params map[string]any `json:"params,omitempty"`
}

// Frame is the shape of each ``inference.*`` message the caller sends on the
// WebSocket. Kept as a plain map so adding fields later does not require
// updating a struct in two places.
type Frame = map[string]any

// Run opens the chat-completions stream, reads it, and emits one Frame per
// ``inference.*`` event. The caller annotates each Frame with request_id
// before writing it to the WebSocket.
//
// Emits ``inference.start`` as soon as the HTTP response headers arrive, then
// ``inference.delta`` for every chunk carrying content, then ``inference.end``
// with the usage counts the local server reported — or no usage at all if the
// server did not return any (llama.cpp historically did not; this just means
// an inference.end with an empty usage object).
//
// On ctx cancellation (``inference.cancel`` or client disconnect), closes the
// HTTP body and returns the ctx error. No further Frames are emitted.
func Run(ctx context.Context, baseURL string, req Request, emit func(Frame)) error {
	payload := buildPayload(req)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	client := &http.Client{
		// A long per-request timeout at the transport layer; the cockpit's
		// idle timeout upstream is 90s and the backoff on reconnect is
		// generous. The real cancellation mechanism is the ctx, which the
		// caller cancels on inference.cancel or disconnect.
		Timeout: 10 * time.Minute,
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("post /v1/chat/completions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}

	emit(Frame{"type": "inference.start"})
	return readStream(ctx, resp.Body, emit)
}

// buildPayload adds the stream/stream_options flags to the request Params
// without clobbering any explicit value the caller set.
func buildPayload(req Request) map[string]any {
	out := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
		// Ask for usage counts to arrive on the final chunk. Servers that do
		// not understand this field ignore it, which is exactly what the
		// forward-compat rule asks for.
		"stream_options": map[string]any{"include_usage": true},
	}
	for k, v := range req.Params {
		if _, taken := out[k]; !taken {
			out[k] = v
		}
	}
	return out
}

// chatCompletionChunk is a subset of OpenAI's chat.completion.chunk — just the
// bits the inference proxy actually reads. Fields missing from a server's
// payload default to the zero value and are handled above.
type chatCompletionChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func readStream(ctx context.Context, body io.Reader, emit func(Frame)) error {
	scanner := bufio.NewScanner(body)
	// Default scanner buffer is 64KiB. A single SSE line fits well under this
	// (a chunk is a few hundred bytes), but a server emitting a 32KiB message
	// with no delta boundary would overflow — raise the ceiling to a MiB to
	// cover misbehaving servers.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var usagePromptTokens, usageCompletionTokens int
	var finishReason string
	usageSeen := false

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			end := Frame{"type": "inference.end"}
			if usageSeen {
				end["usage"] = map[string]any{
					"prompt_tokens":     usagePromptTokens,
					"completion_tokens": usageCompletionTokens,
					"total_tokens":      usagePromptTokens + usageCompletionTokens,
				}
			}
			if finishReason != "" {
				end["finish_reason"] = finishReason
			}
			emit(end)
			return nil
		}
		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A malformed chunk from the local server is dropped rather than
			// terminating the whole exchange — the next chunk may be fine, and
			// stopping on the first glitch would make a flaky local server
			// read as broken end-to-end.
			continue
		}
		if chunk.Usage != nil {
			usagePromptTokens = chunk.Usage.PromptTokens
			usageCompletionTokens = chunk.Usage.CompletionTokens
			usageSeen = true
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
			if choice.Delta.Content != "" {
				emit(Frame{
					"type":    "inference.delta",
					"content": choice.Delta.Content,
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}
	// Stream ended without a [DONE] sentinel. Treat as a clean end — some
	// servers close the connection instead.
	end := Frame{"type": "inference.end"}
	if usageSeen {
		end["usage"] = map[string]any{
			"prompt_tokens":     usagePromptTokens,
			"completion_tokens": usageCompletionTokens,
			"total_tokens":      usagePromptTokens + usageCompletionTokens,
		}
	}
	if finishReason != "" {
		end["finish_reason"] = finishReason
	}
	emit(end)
	return nil
}

// HTTPError is what Run returns when the local server answered with a non-2xx.
// The caller translates it into an ``inference.error`` frame with the local
// server's message, which the backend forwards to the webapp verbatim.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("local server returned %d: %s", e.Status, e.Body)
	}
	return fmt.Sprintf("local server returned %d", e.Status)
}

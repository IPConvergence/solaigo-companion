# Companion ↔ Backend Wire Protocol

The contract between Companion (user side) and Solaigo backend (hosted side). Versioned
so a Companion built against v1 keeps working when the backend is on v2.

## Transport

- **Scheme:** `wss://` only. The backend accepts no `ws://`.
- **URL:** `wss://www.solaigo.com/cockpit/api/companion/ws`
  (dev: `ws://127.0.0.1:8000/api/companion/ws` — the backend relaxes `ws` only when
  bound to loopback).
- **Protocol upgrade:** standard RFC 6455. The backend selects the
  `Sec-WebSocket-Protocol` subprotocol `solaigo.companion.v1`. Companion offers
  `solaigo.companion.v1` and, in the future, any newer version it also understands.
  The backend picks the highest it supports; if there is no overlap, the upgrade is
  refused with HTTP 400.
- **Auth:** the Companion sends `Authorization: Bearer <token>` on the upgrade request.
  No cookies, no query-string tokens. A valid token upgrades to 101; everything else
  is 401 and no WS is opened.
- **Compression:** `permessage-deflate` offered, used when the backend accepts it.
  Streaming deltas benefit most.

## Framing

Every frame is a **text** WebSocket message containing one JSON object. No binary
frames in v1. No partial JSON across multiple frames: one message = one object.

Every object has a `type` field. Fields other than those documented for that `type`
are ignored by both sides on receive, which is the forward-compatible rule — a newer
sender can add a field and an older receiver keeps working.

### Correlation

Any frame that is part of an exchange carries a `request_id`: a string the backend
chose when it originated the request (ULID in practice; the Companion treats it as
opaque). Every frame Companion emits in response uses the same `request_id`. A frame
without a `request_id` is a connection-scoped signal (ping, pong, hello, bye).

## Handshake frames

Immediately after the WS opens, the two sides exchange one frame each.

### `hello` (Companion → backend)

Sent by Companion as its first frame. Advertises what it can do.

```json
{
  "type": "hello",
  "version": "1.0.0",
  "companion_version": "0.1.0",
  "os": "linux",
  "arch": "amd64",
  "hostname": "marcs-laptop",
  "detected": [
    { "server": "ollama", "models": ["llama3.2:latest", "qwen2.5:7b"] }
  ]
}
```

- `version`: protocol version the Companion wants to speak. Must match the subprotocol
  negotiated at upgrade (`solaigo.companion.v1` → `1.x`).
- `companion_version`: the binary version, informational only — appears in the
  cockpit so a user can tell their Companions apart.
- `hostname`: the default display name for the Companion in the cockpit. The user can
  rename it; Companion does not re-publish on reconnect.
- `detected`: zero or more local servers found. Each entry has a `server` tag
  (`ollama`, `lmstudio`, `llamacpp`, `vllm`) and the `models` the server reports.
  Empty means no model is available right now, but the Companion is still connected.

### `welcome` (backend → Companion)

Sent by the backend after a successful `hello`. The Companion must not send inference
responses before receiving `welcome` — the backend may still refuse the session on
policy grounds (e.g. the user's plan was downgraded between claim and connect).

```json
{
  "type": "welcome",
  "session_id": 42,
  "limits": {
    "max_concurrent_requests": 4,
    "max_message_bytes": 262144
  }
}
```

- `session_id`: an integer that identifies this Companion in the cockpit UI and in
  audit logs. Stable across reconnects.
- `limits`: what the backend will enforce on this socket. Companion should refuse
  anything that would exceed them (via `inference.error` with code `limit_exceeded`)
  rather than let the backend close the socket.

If the backend refuses the session, it sends a `bye` (see below) instead of `welcome`
and closes the socket. The Companion then exits with a human message explaining why.

## Inference frames

The protocol's reason to exist.

### `inference.request` (backend → Companion)

```json
{
  "type": "inference.request",
  "request_id": "01HK3R...",
  "model": "llama3.2:latest",
  "server": "ollama",
  "messages": [
    { "role": "system", "content": "You are a careful assistant." },
    { "role": "user", "content": "Say hello in one word." }
  ],
  "params": {
    "temperature": 0.2,
    "max_tokens": 256,
    "stop": ["\n\n"]
  }
}
```

- `model`: as reported in the matching `detected[*].models`. If the Companion no
  longer sees that model (user removed it), it answers with `inference.error`
  code `model_not_found`.
- `server`: which of the detected servers to use. If omitted, Companion uses whichever
  it chose at startup. The backend sends it explicitly when the user has picked a
  specific one in the cockpit.
- `messages`: OpenAI chat-completions message format. Passed through unmodified.
- `params`: a flat object with keys the OpenAI chat-completions endpoint accepts
  (`temperature`, `top_p`, `max_tokens`, `stop`, `presence_penalty`,
  `frequency_penalty`). Companion forwards unknown keys as-is; it does not filter.
  Streaming is always on at the HTTP layer (`stream: true` is added by Companion),
  regardless of what params says.

### `inference.start` (Companion → backend)

Sent once per request, before the first delta. Allows the backend to tell the webapp
that generation has begun — useful because the model may take seconds to produce the
first token on large contexts.

```json
{ "type": "inference.start", "request_id": "01HK3R..." }
```

### `inference.delta` (Companion → backend)

Zero or more, in order. Each carries one chunk of generated text, exactly as it came
out of the local server's SSE stream. The Companion does not buffer or coalesce; it
emits a frame per `data:` line.

```json
{
  "type": "inference.delta",
  "request_id": "01HK3R...",
  "content": "Hello"
}
```

If the chunk includes a tool call (OpenAI tools format), it is carried through in a
`tool_calls` field. V1 of this document does not yet specify tool-call semantics in
detail — the backend forwards them to the webapp and the webapp handles them as it
would for a hosted provider. **Open question** flagged in the TODO at end of file.

### `inference.end` (Companion → backend)

Exactly one per request, after the last delta. Carries usage, if the local server
reports it.

```json
{
  "type": "inference.end",
  "request_id": "01HK3R...",
  "finish_reason": "stop",
  "usage": {
    "prompt_tokens": 23,
    "completion_tokens": 7,
    "total_tokens": 30
  }
}
```

- `finish_reason`: `"stop"`, `"length"`, `"tool_calls"`, or `"content_filter"`.
  Falls back to `"stop"` if the local server does not report one.
- `usage`: omitted if the local server does not report it (llama.cpp does not,
  historically).

### `inference.error` (Companion → backend)

Exactly one per request, in place of `inference.end`, when the exchange cannot
complete.

```json
{
  "type": "inference.error",
  "request_id": "01HK3R...",
  "code": "local_unreachable",
  "message": "Ollama responded with 500: model is still loading",
  "retriable": true
}
```

Error codes v1 defines (the Companion emits exactly one of these):

| Code                 | Meaning                                                        | Retriable |
|----------------------|----------------------------------------------------------------|-----------|
| `local_unreachable`  | Could not reach the local server (connection refused, timeout) | yes       |
| `local_error`        | Local server returned non-2xx                                  | depends on upstream status |
| `model_not_found`    | Requested model is not in the detected list                    | no        |
| `cancelled`          | Request was cancelled before completion                        | no        |
| `limit_exceeded`     | Request violated a limit the backend advertised                | no        |
| `malformed`          | The request itself was invalid                                 | no        |

`retriable: true` tells the backend it can resend the same request on the next
reconnect without the webapp seeing the delay as a hard failure. `false` means
"surface this to the user now."

### `inference.cancel` (backend → Companion)

Sent at any point before `inference.end` or `inference.error` for the same
`request_id`. Companion cancels the in-flight HTTP request to the local server and
emits `inference.error` with code `cancelled` to confirm — the confirmation makes the
cancellation an exchange with an end state, which the backend accounting needs.

```json
{ "type": "inference.cancel", "request_id": "01HK3R..." }
```

## Housekeeping frames

### `models.refresh` (backend → Companion)

Asks Companion to re-probe its local servers and publish an updated `detected` list.
Sent when the user clicks *Re-check* in the cockpit.

```json
{ "type": "models.refresh" }
```

### `models.updated` (Companion → backend)

Sent in response to `models.refresh`, and also unsolicited at most once every 60s when
Companion notices the local list has changed (e.g. the user `ollama pull`-ed a new
model). The payload is the same `detected` array as in `hello`.

```json
{
  "type": "models.updated",
  "detected": [
    { "server": "ollama", "models": ["llama3.2:latest", "qwen2.5:7b", "mistral:7b"] }
  ]
}
```

### `ping` / `pong`

WS-level ping/pong is used in addition to these; these are application-level and let
the backend know Companion is responsive beyond "the TCP connection is still alive."

```json
{ "type": "ping", "t": 1733832000 }
```

```json
{ "type": "pong", "t": 1733832000 }
```

`t` is the sender's unix timestamp, echoed back by the receiver. 30-second interval,
either direction may send. A side that has not received any frame (including pong)
for 90s closes the socket.

### `bye` (either side)

A graceful close. Sent just before closing the WS when the sender has a reason to
share.

```json
{
  "type": "bye",
  "code": "revoked",
  "message": "This Companion was revoked from the cockpit."
}
```

Backend-originated `bye` codes:
- `revoked`: the user removed this Companion from the cockpit.
- `superseded`: another Companion signed in with the same token (should not happen in
  normal operation — the token is single-holder — but the backend handles it by
  evicting the older socket).
- `shutdown`: the backend is restarting; Companion should reconnect.
- `incompatible`: the Companion's protocol version is no longer accepted.

Companion-originated:
- `local_lost`: the local LLM server has been unreachable for more than N minutes and
  the user should be told. Backend surfaces this as the connection error on the card.
- `shutdown`: user quit Companion.

After `bye`, both sides close the WS with code 1000.

## Versioning

The subprotocol token (`solaigo.companion.v1`) and the `version` field in `hello`
are the two signals. Backward-compatible additions bump the patch/minor part of
`version` but keep the subprotocol token the same. A breaking change bumps the token
(`solaigo.companion.v2`), and both sides may offer multiple tokens at upgrade; the
backend picks the highest it accepts.

New fields are added forward-compatibly: unknown fields are ignored on receive. New
`type` values arriving at an older receiver are logged and dropped — the exchange
they belong to ends in the receiver's own timeout, which is why every exchange has
end states (`inference.end`, `inference.error`) and the backend's accounting handles
missing end as a timeout.

## Open questions (to resolve before V1 ships)

- **Tool calls.** Backend and webapp already handle OpenAI tool calls for hosted
  providers. Ollama's OpenAI shim supports them unevenly across models. The protocol
  carries them through, but a model that refuses or malforms them is a case this doc
  has not nailed down. Likely resolution: Companion forwards verbatim, including
  malformed, and the backend's existing error handling takes it.
- **Image inputs.** Base64-encoded images in messages are already large; a WS text
  frame can hold them (`max_message_bytes` default is 256 KiB — a resized photo
  fits), but a 10 MB raw photo does not. V1 will reject messages whose encoded size
  exceeds `max_message_bytes` with `malformed`; a dedicated binary-frame upload channel
  is a V1.1 topic.
- **Request routing when the user has multiple Companions.** Deferred to V2 (one
  Companion per user is the V1 constraint; the cockpit UI will enforce it).

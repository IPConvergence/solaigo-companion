# Backend Changes — Phase 2

What the Solaigo cockpit (repo: `IPConvergence/trust-ai-cockpit`) needs on its side
to make Companion work. **This document is a plan**, not a diff. Phase 2 implements
it.

## Summary

Two new tables, five new routes, one new value in an existing enum, one new branch in
the agent-turn code path. No breaking changes to anything that already works.

## Tables

Two new tables. Both migrated under `backend/migrations/versions/`, one revision.

### `companion_pairings`

The short-lived codes shown in the cockpit before a Companion claims them.

```
id                    integer, PK
user_id               integer, FK users.id, not null, indexed
code_hash             text, not null, unique       -- SHA-256 of the shown code
created_at            datetime, not null
expires_at            datetime, not null           -- created_at + 10 minutes
claimed_at            datetime, nullable           -- set when exchanged
claim_ip              text, nullable               -- source IP of the claim, kept for one audit cycle
```

The code itself is **never stored**. The cockpit generates a 6-character code
(`[A-HJ-NP-Z2-9]{3}-[A-HJ-NP-Z2-9]{3}` — crockford-ish, no visually ambiguous
characters), shows it to the user, hashes it, writes the hash. On claim the backend
hashes the submitted code and looks it up.

Rows are deleted after 24 hours (claimed or not) by a periodic sweep already present
for other short-lived tables. The claim-IP is dropped at the same cadence.

### `companion_sessions`

The long-lived credentials and the view of currently-connected Companions.

```
id                    integer, PK
user_id               integer, FK users.id, not null, indexed
display_name          text, not null, max 80       -- defaulted from the hello hostname, user-renamable
token_hash            text, not null, unique       -- SHA-256 of the opaque token
protocol_version      text, not null               -- from hello.version
companion_version     text, not null               -- informational
os                    text, not null
arch                  text, not null
detected              text, nullable               -- JSON: [{server, models: [...]}]
created_at            datetime, not null
last_seen_at          datetime, nullable           -- updated on every welcome
revoked_at            datetime, nullable           -- set when the user revokes
```

Issuing a token: generate 32 cryptographic bytes, base64-url, hash with SHA-256,
store the hash. The token in the clear is returned exactly once, in the claim
response, and never again — the same rule as for API keys.

`detected` is kept so the cockpit UI can render the Connections card even when the
Companion is briefly offline. Updated on every `hello` and on every `models.updated`
frame.

## Routes

All under `/api/companion/`. Mounted in `backend/app/api/routes_companion.py`.

### `POST /api/companion/pair`

Authenticated. Called by the cockpit webapp when the user clicks *Pair a Companion*.

Request: empty body.

Response:
```json
{
  "code": "AB4-3K9",
  "expires_at": "2026-10-09T14:12:00Z"
}
```

The code is shown to the user once, through this response, and never read back from
the backend. Rate-limited per user to 5 codes per hour (a user asking for a sixth
code almost certainly has a stuck pairing — the webapp invalidates the previous
unclaimed codes when the user issues a new one, so the limit is a safety net rather
than a workflow gate).

### `POST /api/companion/claim`

**Unauthenticated.** Called by the Companion binary with the user-typed code.

Request:
```json
{
  "code": "AB4-3K9",
  "companion_version": "0.1.0",
  "os": "linux",
  "arch": "amd64",
  "hostname": "marcs-laptop"
}
```

Response (success):
```json
{
  "token": "wF3-mN...",
  "wss_url": "wss://www.solaigo.com/cockpit/api/companion/ws",
  "session_id": 42
}
```

Response (failure): HTTP 400 with a `detail` message. The code can fail for three
reasons — not found, expired, already claimed — and the response does not
distinguish between them, which closes an enumeration oracle a public endpoint
should not open. The user sees the same message in all three cases: *That code is
not valid. Open the cockpit again and get a fresh one.*

Rate-limited per source IP: 10 attempts per minute, 60 per hour. The limiter is
shared with the login endpoint's limiter.

### `GET /api/companion/sessions`

Authenticated. Lists the user's Companions.

Response:
```json
{
  "sessions": [
    {
      "id": 42,
      "display_name": "marcs-laptop",
      "os": "linux",
      "arch": "amd64",
      "companion_version": "0.1.0",
      "last_seen_at": "2026-10-09T14:10:11Z",
      "connected": true,
      "detected": [
        { "server": "ollama", "models": ["llama3.2:latest"] }
      ]
    }
  ]
}
```

`connected` is derived from an in-memory map of session_id → open WS, not from the
database — the DB knows the token was issued, not that a socket is open right now.

### `PATCH /api/companion/sessions/{id}`

Authenticated. Rename the Companion.

Request:
```json
{ "display_name": "office NUC" }
```

### `DELETE /api/companion/sessions/{id}`

Authenticated. Revoke the token and force-close the WS if open.

The flow:
1. Set `revoked_at = now()` on the row.
2. If the session_id is in the in-memory map, send `{"type":"bye","code":"revoked",...}`
   on the WS and close it.
3. The Companion sees the close, calls the claim endpoint on next start-up (its token
   is already gone from the server), gets a 401 from the WS upgrade in the meantime
   if it reconnects before being killed by its service manager.

Deletion is soft (`revoked_at`) rather than hard: the row is kept for the audit log
window, same as other credential tables.

### `GET /api/companion/ws`

**The WebSocket.** Authentication is `Authorization: Bearer <token>` on the upgrade
request. On accept:
1. Hash the token, look up the row. If not found or revoked, close with 1008.
2. Read the `hello` frame (with a short timeout). Validate the protocol version.
3. Store the Companion in the in-memory `session_id → WS` map.
4. Send `welcome`.
5. Stream frames per `PROTOCOL.md`.

The in-memory map is per-process. In production this is one uvicorn worker; if more
workers become needed, a cross-process router (Redis pub/sub, or a sticky-upstream
nginx config) is added at that point — V1 does not need it.

## `AiConnection` changes

The existing table (`backend/app/tables.py:703`) gets a new `deployment_type` value:
`"companion"`. The endpoint field holds `companion://{session_id}` for these rows —
unparseable as HTTP, so any code path that still reaches `endpoints.normalise()` with
one bails cleanly; the agent-turn code path (see below) sees the schema and routes
elsewhere before URL parsing happens.

A new column: `companion_session_id` (integer, FK `companion_sessions.id`, nullable).
It is required when `deployment_type = 'companion'` and null otherwise. A CHECK
constraint enforces this.

The Pydantic model `ConnectionCreate` grows a validation branch: if the submitted
form has `deployment_type = 'companion'`, the request must carry a `session_id` that
belongs to the user, and both `endpoint` and `api_key` must be absent.

Grade: a companion-type connection grades on the same declarations the custom
endpoint already does (jurisdiction + region + model-is-open), with one addition —
the signed Companion binary **is** evidence of deployment (the one piece
`CLAUDE.md:360-369` reserved under `deployment_evidence_validated`). The grade can
reach L5 for an open-weights model running on self-declared EU hardware under a
signed Companion. The seam is finally bolted to something.

## `agents.py` changes

The agent turn (`backend/app/agents.py`, currently around lines 307-344) grows one
branch:

```
if connection.deployment_type == "companion":
    yield from companion_proxy.run(connection, request)
else:
    yield from existing_http_path(connection, request)
```

`companion_proxy.run(connection, request)`:
1. Look up `connection.companion_session_id` in the in-memory WS map.
2. If offline: raise, `agents.py` turns that into the usual 503-equivalent the webapp
   handles.
3. Mint a `request_id`, send `inference.request` on the WS with the model, messages,
   params from the request.
4. Read frames back: `inference.start` (ignored by this layer, surfaces to the
   webapp as `thinking` continues), `inference.delta` (converted into the same SSE
   frames the hosted path emits), `inference.end` or `inference.error`.
5. On webapp cancellation (abort of the SSE stream), send `inference.cancel`.

The webapp sees exactly the same event stream as from a hosted provider. The
companion branch is pure routing.

## Order of implementation

Suggested order for Phase 2, each a separate PR that can ship on its own:

1. Migration + models + `pair` + `claim` + `sessions` + `DELETE`. No WS yet.
   Cockpit UI shows a *Pair a Companion* button, the pairing works end-to-end, the
   Companion stops at "paired, nothing to do." Nothing routes through this yet.
2. WS endpoint + `hello`/`welcome`/`bye` + the in-memory map. Keepalive. A Companion
   can connect and stay connected. Still nothing routes through it.
3. `AiConnection` changes and the UI to add a connection pointing at a Companion.
   The connection grades, appears in the cockpit, but has no runtime effect yet
   (hosted path still used if the user has one, Companion path raises not-implemented
   if the user picks it).
4. `agents.py` branch and the `companion_proxy`. The feature is live.
5. Error states and polish: offline card in the UI, re-check button, re-pairing flow
   when a token is lost.

The first three can land without touching the webapp's chat path, which keeps the
blast radius small — a bug in the Companion plumbing cannot break the hosted
providers that already work.

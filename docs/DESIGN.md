# Solaigo Companion — Design

A desktop app that lets a Solaigo user connect a language model running on their own
computer. One binary per operating system, no runtime to install, pair-once-and-forget.

The cockpit itself proxies every model call from its own server, which is correct for
every provider except the one that lives behind the user's NAT: their laptop is not
reachable from ours, and the straight answer in that case was a cryptic 400. Companion
is the piece that closes the gap, by **opening a connection from the user's machine to
Solaigo** and letting the backend push inference requests back down it. The reachability
question stops being the user's problem.

## Non-goals

What this is deliberately not:

- **Not a chat UI.** The cockpit remains the surface the user sees. Companion has no
  window, no menu, no settings panel of its own after pairing. It is a service, like
  Dropbox's or 1Password's agent.
- **Not an LLM runtime.** It does not download weights, it does not serve inference. It
  speaks to a local server the user already runs (Ollama, LM Studio, llama.cpp, vLLM) —
  all of which expose OpenAI-compatible `/v1/chat/completions`.
- **Not a tunneling service.** It does not punch holes in the user's network. The only
  socket it opens is outbound to `wss://www.solaigo.com/cockpit/api/companion/ws`.
- **Not a key manager.** The user's LLM key (if any — Ollama ships without one) stays in
  the Companion config on their machine and never travels to Solaigo. The Solaigo-issued
  pairing token is the only credential Solaigo mints.

## Architecture at a glance

```
   ┌───────────────────┐     wss (persistent)      ┌─────────────────────┐
   │  Solaigo backend  │ ◄─────────────────────────┤  Solaigo Companion  │
   │  (hosted)         │   inference.request       │  (user machine)     │
   │                   │ ─────────────────────────►│                     │
   │  agents.py routes │   inference.delta/end     │                     │
   │  to WS when the   │ ◄─────────────────────────┤                     │
   │  connection is a  │                           │                     │
   │  companion        │                           │  calls local HTTP   │
   └───────────────────┘                           │  :11434 (Ollama)    │
            ▲                                      │  :1234  (LM Studio) │
            │ https                                │  :8080  (llama.cpp) │
            │                                      │  :8000  (vLLM)      │
   ┌───────────────────┐                           └─────────────────────┘
   │  Browser (webapp) │
   └───────────────────┘
```

Four things to notice about the picture:

1. **The arrow points the right way.** Companion is the party that opens the connection,
   because the one that opens is the one that can be anywhere behind any firewall. The
   backend never dials the user's machine; it answers an upgrade request on `:443` like
   any other HTTPS endpoint.
2. **The connection is persistent and bidirectional.** HTTP long-polling would also work
   and would be simpler to debug; WebSockets are chosen because streaming inference is a
   V1 goal (see §Phasing) and multiplexing deltas over a poll loop is a protocol one
   doesn't want to invent.
3. **The browser does not change.** The cockpit webapp sends messages exactly as it does
   today; agents.py routes to the WS instead of HTTPS when the connection is a Companion
   one, and the browser is none the wiser. The user's local URL never travels anywhere.
4. **The Companion never calls the cockpit.** Every message on the WS is a response or a
   stream frame. There is no "pull" direction: the user's machine never gets to ask the
   server anything that is not an answer to something it was asked. This is deliberate
   and keeps the trust boundary small.

## Pairing

Non-technical users do not generate device codes or paste OAuth URLs. The flow is:

1. In the cockpit, under *Connexions*, the user clicks **Pair a Companion**.
2. The cockpit shows a short code — six characters, groups of three, like `AB4-3K9`. It
   is valid for ten minutes and consumable once.
3. The user installs Companion if they have not, runs `solaigo-companion login` (or
   opens the installer which runs it), and types the code when asked.
4. Companion POSTs the code to `/api/companion/claim` over HTTPS. The backend verifies
   it, mints a long-lived opaque token (32 bytes, base64), binds it to the user, and
   returns it along with the WSS URL.
5. Companion saves the token to its config (see §Storage) and immediately opens the WS.
6. The cockpit shows the Companion as *paired* within the same screen, named by its
   hostname by default. The user can rename it or revoke it from there.

The code is not an OAuth device code. OAuth device flow is standard, but it means a
real OAuth provider on the backend, consent screens, token refresh, scopes — and
Solaigo has none of this today. The pairing-code path is simpler, is one HTTP route to
add on the backend, and has the same security properties for the scope we need: proof
that the person running Companion is the same person logged into the cockpit, right
now. If OAuth becomes needed later (for third-party integrations), it is added without
removing this.

**Why not a QR code.** QR works for mobile; a desktop user has their keyboard right
there and most desktops have no camera facing them. Typing six characters is faster.

**Why six characters.** Short enough to say out loud over a phone to a colleague setting
one up, long enough that online guessing is pointless at the rate limit the backend will
impose (10 attempts per IP per minute, which is already far more than a legitimate flow
needs).

## Session lifecycle

The WS stays open as long as Companion is running and the token is valid. The backend
treats a disconnect as transient: the token is not invalidated by a lost socket. The
only things that invalidate it are the user clicking *Revoke* in the cockpit, or the
backend seeing the token hash removed from the `companion_sessions` table.

On reconnect, Companion identifies itself with the same token. The backend binds the
new socket to the same session row and resumes. In-flight inference requests are not
replayed — a request whose response was interrupted ends as `inference.error` on the
browser side with a message the webapp already handles.

**Keepalive.** The WS ping/pong at 30s intervals; the backend closes any socket silent
for 90s and relies on the Companion to reopen. The webapp's existing agent-timeout
(90s, documented in cockpit CLAUDE.md:1140-1167) covers the race.

**Revoke** is immediate. The backend force-closes the WS and marks the token row
revoked. Companion sees the close, tries to reconnect, gets 401, exits with a message
telling the user to re-pair from the cockpit if that was not intended.

## Local LLM discovery

On start, Companion probes four addresses in order and records which ones answer:

| Server     | Address              | How we check                   |
|------------|----------------------|--------------------------------|
| Ollama     | `http://127.0.0.1:11434/api/tags` | 200 OK → read models      |
| LM Studio  | `http://127.0.0.1:1234/v1/models` | 200 OK → read models      |
| llama.cpp  | `http://127.0.0.1:8080/v1/models` | 200 OK → read models      |
| vLLM       | `http://127.0.0.1:8000/v1/models` | 200 OK → read models      |

If exactly one answers, that is the one Companion uses. If several answer, Companion
picks Ollama (most common, lowest-friction install) by default and reports the full
list so the cockpit can let the user pick. If none answer, Companion stays connected,
reports *no model available*, and the cockpit surfaces that in the Connections card.

The address is `127.0.0.1`, not `localhost`. Hostname resolution is not something a
local-only probe should wait on, and `localhost` resolution has been the cause of
surprising delays on badly-configured machines in the past.

## Inference proxy

The protocol (see `PROTOCOL.md`) covers the frame shapes. The behaviour, in words:

- The backend sends `inference.request` with the model name, the messages, and
  `stream: true` (V1 streams by default — see §Phasing).
- Companion POSTs the same body to the local LLM at
  `{detected-base-url}/v1/chat/completions` with `stream: true`.
- Each OpenAI SSE `data:` line becomes an `inference.delta` frame. The local LLM's
  final `data: [DONE]` plus usage summary becomes `inference.end`.
- If the local server errors or disconnects, Companion sends `inference.error` with
  the status code and the body if any; the backend turns that into the usual error
  the webapp already renders.
- The backend can send `inference.cancel` at any time (e.g. the user closed the tab);
  Companion cancels the in-flight HTTP request and does not send any further frames
  for that request_id.

Companion speaks only the OpenAI chat-completions shape. Ollama has its own `/api/chat`
with a slightly different wire format; Companion uses Ollama's OpenAI-compatible path
(`/v1/chat/completions`), which Ollama has shipped since 0.1.17 and which keeps us from
building a translation layer we would otherwise have to maintain for every backend.

## Threat model

What this defends against, and what it does not:

| Threat | Covered? | How, or why not |
|--------|----------|-----------------|
| An attacker on the public internet reaching the user's LLM | ✓ | No inbound port. The only socket is Companion → backend. |
| A compromised Solaigo backend reading the user's LLM responses | ✗ | Out of scope. The backend has to see responses — it is the agent orchestrator. The privacy notice says plainly that Solaigo processes requests. |
| A compromised Solaigo backend exfiltrating the user's filesystem via Companion | ✓ | Companion speaks exactly one protocol and does nothing but proxy inference. There is no shell, no file access, no `exec` frame. |
| A leaked pairing token used from another machine | partial | The token is bound to the user, not the machine. If leaked, an attacker with the token can send inference requests on the user's behalf, which costs them tokens on their own local LLM, not money. User revokes from the cockpit. |
| A leaked pairing code (the short one, pre-claim) | ✓ | 10-minute TTL, one-time, rate-limited by IP on claim. |
| An attacker on the user's LAN reaching the local LLM | ✗ | Out of scope. The LLM listens on 127.0.0.1 by default in all four servers listed above; if the user has reconfigured it to listen on 0.0.0.0, that is a decision Companion does not reach into. |
| A malicious Companion binary (supply chain) | partial | Releases are signed by the GitHub Actions release workflow. The user is encouraged to install from the GitHub releases page, not from a third-party mirror. Code signing on macOS/Windows is a V1.1 goal. |

The one thing worth repeating out loud: Solaigo can read the content of inference
requests and responses, because the agent lives on the backend. Companion does not
change this and does not try to pretend otherwise. The property Companion gives the
user is **reachability without exposure**, not **end-to-end encryption of chat
content**. That is a separate conversation and a different design.

## Config and storage

Companion keeps two things on disk:

- **A token file.** `${XDG_CONFIG_HOME:-~/.config}/solaigo-companion/token` on Linux,
  `~/Library/Application Support/solaigo-companion/token` on macOS,
  `%AppData%\solaigo-companion\token.dat` on Windows. Mode `0600` on POSIX, user-only
  ACL on Windows. Contains the token and the WSS URL.
- **A small state file** next to it, with the detected server (so a restart does not
  re-probe immediately) and a counter of recent reconnects (for backoff).

That is all. No conversation history, no model weights, no logs beyond what the OS
service subsystem keeps for stderr. If a user removes those two files, Companion is
back to unpaired state and the next run will prompt for a code.

## OS lifecycle

Companion runs as a user-level service:

- **Linux:** systemd user unit (`systemctl --user enable solaigo-companion`). Starts
  on session login, restarts on failure with exponential backoff.
- **macOS:** `launchd` user agent (`~/Library/LaunchAgents/com.solaigo.companion.plist`).
  Loads on login, `KeepAlive = true`.
- **Windows:** installed as a per-user scheduled task running at logon, pointing at
  `%LocalAppData%\Programs\solaigo-companion\solaigo-companion.exe`.

The installer places the unit files and starts the service. Uninstall removes them.
No root privileges are needed at any step.

The CLI (`solaigo-companion`) is also directly runnable from a terminal, which is how
the first version will be tested before the installers are built.

## Phasing

V1 (this quarter, the thing we are building):

- Pair with a code
- Detect Ollama only
- Proxy streaming inference, non-streaming fallback if the local server lacks SSE
- Cockpit shows the Companion in Connections, with its name and status
- `solaigo-companion login`, `logout`, `status`, `pair`

V1.1 (next):

- Detect LM Studio, llama.cpp, vLLM
- Model picker in the cockpit when several models are offered
- Code signing on macOS and Windows
- Auto-update

V2 (speculative):

- Companion on a second machine on the user's LAN (same architecture, different box)
- Access to local tool-calling (filesystem reads under a declared root, local search)
  — opens a different threat conversation and will have its own design doc

## Why Go

One binary per OS, cross-compiled from a single Linux CI machine, no runtime on the
user side, standard library covers HTTP/TLS/WS/JSON, mature ecosystem for service
plumbing (`kardianos/service` handles systemd/launchd/Windows service in one API). The
closest alternatives are Rust (longer compile times, steeper learning curve for this
team's current composition) and Node via `pkg`/`bun compile` (bigger binaries, less
clean story for Windows service). Go is the boring choice, and this is a service that
should be boring.

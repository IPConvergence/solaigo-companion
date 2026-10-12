# Solaigo Companion

The desktop companion for Solaigo. It bridges a language model running on your own
computer to your Solaigo account, so a model you host locally is reachable from the
cockpit the same way a hosted provider is — without opening any port, running any
tunnel, or putting your laptop on the public internet.

**Status: alpha.** Pairing, the live connection, local inference through Ollama and
the per-OS service are all in place. Design and wire protocol are in [`docs/`](docs/).

## What it does

- Opens one outbound WebSocket from your machine to `www.solaigo.com`.
- Detects a local OpenAI-compatible server (Ollama, LM Studio, llama.cpp, vLLM).
- Receives inference requests from the Solaigo backend, forwards them to the local
  server, streams the response back.
- Runs as a user-level service — systemd on Linux, launchd on macOS, the Run key on
  Windows — installed without administrator rights.

What it is **not**: a chat UI, an LLM runtime, a tunneling service, a key manager.
See [`docs/DESIGN.md § Non-goals`](docs/DESIGN.md#non-goals).

## Install

In your Solaigo cockpit: **AI connections → Pair a Companion**. It shows one command
for your system with your pairing code already in it, for example:

```sh
curl -fsSL https://www.solaigo.com/cockpit/companion/install.sh | sh -s -- --code ABC-123
```

```powershell
& ([scriptblock]::Create((irm https://www.solaigo.com/cockpit/companion/install.ps1))) -Code ABC-123
```

The script picks the binary for your system and processor, checks it against the
release's `SHA256SUMS`, pairs it, and runs `install`, which copies it somewhere stable
and starts it now and at every login. No administrator rights are needed. A file
fetched this way carries no "downloaded from the internet" mark, so neither Gatekeeper
nor SmartScreen stops it — which is also why the binaries are not code-signed yet.
Windows 11's Smart App Control, on the machines where it is switched on, can still
refuse an unsigned program; signing is what would answer that.

By hand: download the binary for your platform from the releases page, then

```sh
./solaigo-companion-<platform> pair --code ABC-123
./solaigo-companion-<platform> install
```

`uninstall` removes the service; `logout` forgets the token on this machine.

## Build from source

Requires Go 1.27 or newer.

```sh
make build          # produces ./solaigo-companion for the host OS
make test           # runs the test suite
make cross          # linux, macOS, windows — amd64 and arm64 — plus SHA256SUMS
make help           # lists every target
```

## Repository layout

```
cmd/companion/        The CLI: pair, status, logout, daemon, install, uninstall.
docs/                 DESIGN, PROTOCOL, and the backend-side plan.
internal/             claim, config, detect, proxy, transport.
.github/workflows/    CI (build + test on Linux, macOS, Windows).
```

The counterpart backend changes live in [`IPConvergence/trust-ai-cockpit`](https://github.com/IPConvergence/trust-ai-cockpit)
(the Solaigo cockpit itself). [`docs/BACKEND_CHANGES.md`](docs/BACKEND_CHANGES.md)
spells out what Phase 2 adds there.

## License

Apache License 2.0. See [`LICENSE`](LICENSE). Permissive, patent-grant included, no
copyleft reach into applications that use Companion as a dependency.

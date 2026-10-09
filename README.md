# Solaigo Companion

The desktop companion for Solaigo. It bridges a language model running on your own
computer to your Solaigo account, so a model you host locally is reachable from the
cockpit the same way a hosted provider is — without opening any port, running any
tunnel, or putting your laptop on the public internet.

**Status: Phase 1 — specification and scaffolding.** This repository compiles and
runs, but the subcommands are stubs. The design and the wire protocol are frozen in
[`docs/`](docs/); Phase 2 is the implementation.

## What it does

- Opens one outbound WebSocket from your machine to `www.solaigo.com`.
- Detects a local OpenAI-compatible server (Ollama, LM Studio, llama.cpp, vLLM).
- Receives inference requests from the Solaigo backend, forwards them to the local
  server, streams the response back.
- Runs as a user-level service — systemd on Linux, launchd on macOS, scheduled task
  on Windows.

What it is **not**: a chat UI, an LLM runtime, a tunneling service, a key manager.
See [`docs/DESIGN.md § Non-goals`](docs/DESIGN.md#non-goals).

## Build from source

Requires Go 1.27 or newer.

```sh
make build          # produces ./solaigo-companion for the host OS
make test           # runs the test suite
make cross          # builds for linux-amd64, darwin-amd64, darwin-arm64, windows-amd64
make help           # lists every target
```

## Try the scaffold

```sh
./solaigo-companion            # prints usage
./solaigo-companion version    # prints the version string
./solaigo-companion pair       # not implemented yet
```

## Pair with your Solaigo account

Phase 2. For now:

1. Build the binary with `make build`.
2. In the Solaigo cockpit, under **Connexions**, you will click *Pair a Companion*
   and see a short code. (This UI arrives in Phase 2 alongside the pairing endpoint.)
3. `./solaigo-companion pair` will ask for the code and claim a long-lived token.
4. The Companion stays connected and your local LLM appears as a connection in the
   cockpit.

Design (why) and protocol (how) are in [`docs/`](docs/).

## Repository layout

```
cmd/companion/        The CLI entry point. The scaffold is here today.
docs/                 DESIGN, PROTOCOL, and the backend-side plan for Phase 2.
internal/             Phase 2: pair, transport, detect, proxy. Empty for now.
.github/workflows/    CI (build + test on Linux, macOS, Windows).
```

The counterpart backend changes live in [`IPConvergence/trust-ai-cockpit`](https://github.com/IPConvergence/trust-ai-cockpit)
(the Solaigo cockpit itself). [`docs/BACKEND_CHANGES.md`](docs/BACKEND_CHANGES.md)
spells out what Phase 2 adds there.

## License

Apache License 2.0. See [`LICENSE`](LICENSE). Permissive, patent-grant included, no
copyleft reach into applications that use Companion as a dependency.

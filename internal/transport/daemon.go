package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"runtime"
	"time"

	"github.com/IPConvergence/solaigo-companion/internal/config"
)

// Reconnect backoff bounds. First retry fast (one second) because transient
// network blips are the common case; cap at a minute so a Companion left
// running overnight through a brief internet outage reconnects promptly when
// it comes back.
const (
	MinBackoff = 1 * time.Second
	MaxBackoff = 60 * time.Second
)

// LoopOptions bundles what Loop needs. Separated so tests can inject a
// LocalDiscoverer that returns a fixed detected list without probing a real
// Ollama (Phase 2 Step 4 will supply the real one).
type LoopOptions struct {
	Token            config.Token
	CompanionVersion string
	Hostname         string
	Discover         func() []DetectedServer
	// Where diagnostics go. Nil means log.Default().
	Log *log.Logger
}

// Loop opens the WS, runs it until it ends, then reconnects — forever, unless
// ``ctx`` is cancelled or the token is refused (which no amount of retry can
// fix). SIGINT and SIGTERM come in as a cancelled ``ctx`` from the caller.
//
// Returns nil on a clean shutdown (ctx.Done) and the terminal error otherwise.
// An auth failure is terminal: a revoked or missing token is not a transient
// issue, and continuing to pound /ws every few seconds would be noise in the
// server logs and nothing more.
func Loop(ctx context.Context, opts LoopOptions) error {
	logger := opts.Log
	if logger == nil {
		logger = log.Default()
	}
	backoff := MinBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}
		detected := []DetectedServer{}
		if opts.Discover != nil {
			detected = opts.Discover()
		}
		session, err := Dial(
			ctx, opts.Token, opts.CompanionVersion, opts.Hostname,
			runtime.GOOS, runtime.GOARCH, detected,
		)
		if err != nil {
			if IsAuthFailure(err) {
				logger.Printf("auth failed: %v — re-pair this Companion from the cockpit", err)
				return err
			}
			logger.Printf("connect failed: %v (retry in %v)", err, backoff)
			if err := sleep(ctx, backoff); err != nil {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}

		logger.Printf("connected: session %d (limits=%v)", session.SessionID, session.Limits)
		backoff = MinBackoff

		err = session.Run(ctx)
		_ = session.Close("daemon loop ended")
		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
			logger.Printf("session ended cleanly")
			return nil
		}
		logger.Printf("session ended: %v (reconnect in %v)", err, backoff)
		if err := sleep(ctx, backoff); err != nil {
			return nil
		}
		backoff = nextBackoff(backoff)
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > MaxBackoff {
		next = MaxBackoff
	}
	return next
}

// sleep returns nil after the delay, or the context's error if it was
// cancelled first.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ErrNotPaired is what the daemon subcommand surfaces when it is launched with
// no local token. Distinguished so the caller can print a human message rather
// than a stack of library errors.
var ErrNotPaired = fmt.Errorf("not paired: run `solaigo-companion pair` first")

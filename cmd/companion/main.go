// Package main is the Solaigo Companion entry point.
//
// Phase 2 Step 3: ``daemon`` opens the WebSocket to the cockpit and keeps it
// open with reconnect-and-backoff. ``install`` and ``uninstall`` register the
// binary as a user-level service on each OS so the daemon runs on login
// without the user having to think about it.
//
// Design: docs/DESIGN.md. Protocol: docs/PROTOCOL.md.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/IPConvergence/solaigo-companion/internal/claim"
	"github.com/IPConvergence/solaigo-companion/internal/config"
	"github.com/IPConvergence/solaigo-companion/internal/transport"
)

// Set by the release workflow via -ldflags "-X main.version=...". The default is
// what someone running `go build` out of a working tree sees.
var version = "0.0.0-dev"

// Where the Companion dials by default. A single setting rather than a build-time
// choice so a developer can point at a local cockpit with ``--server`` and nothing
// else changes.
const defaultServer = "https://www.solaigo.com/cockpit"

const usage = `solaigo-companion — bridge a local LLM to your Solaigo account

Usage:
  solaigo-companion <command> [arguments]

Commands:
  pair       Enter a pairing code from the cockpit and claim a token
  login      Alias for pair, named for people who expect it
  status     Show the pairing state and the server this Companion dials
  logout     Clear this Companion's token locally; the cockpit still shows it
             until you remove it there
  daemon     Keep an open WebSocket to the cockpit. Runs in the foreground;
             the install command registers it as a background service
  install    Register this binary as a user-level service that runs on login
  uninstall  Remove the service registration
  version    Print the version string

Common flags:
  --server URL   Cockpit base URL (default: https://www.solaigo.com/cockpit)
  --code  CODE   Pairing code, so you can script it instead of typing it in

Run 'solaigo-companion <command> -h' for subcommand help.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "version", "-v", "--version":
		fmt.Println(version)
	case "pair", "login":
		if err := runPair(args, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "pair:", err)
			os.Exit(1)
		}
	case "status":
		if err := runStatus(args, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "status:", err)
			os.Exit(1)
		}
	case "logout":
		if err := runLogout(args, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "logout:", err)
			os.Exit(1)
		}
	case "daemon":
		if err := runDaemon(args, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "daemon:", err)
			os.Exit(1)
		}
	case "install":
		if err := runInstall(args, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "install:", err)
			os.Exit(1)
		}
	case "uninstall":
		if err := runUninstall(args, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "uninstall:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// runPair claims a code and saves the resulting token. Separated from main() so a
// test can drive it with a fake stdin and a captured stdout, without touching the
// process's real file descriptors.
func runPair(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(out)
	server := fs.String("server", defaultServer, "cockpit base URL")
	code := fs.String("code", "", "pairing code (prompted if omitted)")
	force := fs.Bool("force", false, "overwrite an existing paired token without asking")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Refuse to overwrite an existing token silently: pairing again on top of a
	// working token would leak the previous one from this machine's state (the
	// cockpit still has it) and the user who ran it twice would not know.
	if existing, err := config.Load(); err == nil && existing != nil && !*force {
		return fmt.Errorf(
			"this Companion is already paired to %s (session %d). "+
				"Run `solaigo-companion logout` first, or re-run with --force to replace it",
			existing.Server, existing.SessionID,
		)
	}

	if *code == "" {
		fmt.Fprint(out, "Pairing code (from your Solaigo cockpit): ")
		typed, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("could not read the code: %w", err)
		}
		*code = strings.TrimSpace(typed)
	}
	*code = strings.ToUpper(strings.TrimSpace(*code))
	if *code == "" {
		return errors.New("no code given")
	}

	req := claim.Request{
		Code:             *code,
		CompanionVersion: version,
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		Hostname:         config.Hostname(),
	}

	fmt.Fprintln(out, "Claiming code with", *server, "…")
	resp, err := claim.Claim(*server, req, 15*time.Second)
	if err != nil {
		return err
	}

	t := config.Token{
		Server:    strings.TrimRight(*server, "/"),
		Token:     resp.Token,
		WSSURL:    resp.WSSURL,
		SessionID: resp.SessionID,
	}
	if err := config.Save(t); err != nil {
		return fmt.Errorf("could not save the token: %w", err)
	}

	path, _ := config.TokenPath()
	fmt.Fprintf(out, "Paired. Token saved to %s (mode 0600).\n", path)
	fmt.Fprintln(out, "The Companion will appear in your cockpit under 'Compagnons' as", req.Hostname+".")
	fmt.Fprintln(out, "Phase 2 Step 3 brings the live connection; today, nothing stays running.")
	return nil
}

func runStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}

	t, err := config.Load()
	if errors.Is(err, config.ErrNoToken) {
		fmt.Fprintln(out, "Not paired. Run `solaigo-companion pair` to get started.")
		return nil
	}
	if err != nil {
		return err
	}
	path, _ := config.TokenPath()
	fmt.Fprintln(out, "Paired.")
	fmt.Fprintln(out, "  Server     :", t.Server)
	fmt.Fprintln(out, "  Session ID :", t.SessionID)
	fmt.Fprintln(out, "  WSS URL    :", t.WSSURL)
	fmt.Fprintln(out, "  Token file :", path)
	// The token value itself is deliberately not printed, same reason the cockpit
	// never sends it back out: a terminal buffer, a tmux scrollback, a screen share
	// — four places a credential should not land for the sake of a status line.
	return nil
}

func runLogout(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	fs.SetOutput(out)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}

	t, err := config.Load()
	if errors.Is(err, config.ErrNoToken) {
		fmt.Fprintln(out, "Not paired. Nothing to clear.")
		return nil
	}
	if err != nil {
		// The file exists but is unreadable — delete it anyway, the user asked.
		fmt.Fprintln(out, "The token file is unreadable; clearing it.")
	}

	if !*yes {
		fmt.Fprint(out, "Clear the local token")
		if t != nil {
			fmt.Fprintf(out, " (session %d on %s)", t.SessionID, t.Server)
		}
		fmt.Fprint(out, "? [y/N] ")
		typed, _ := bufio.NewReader(in).ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(typed)), "y") {
			fmt.Fprintln(out, "Kept.")
			return nil
		}
	}

	if err := config.Delete(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Local token cleared.")
	fmt.Fprintln(out, "The cockpit still shows this Companion until you remove it there.")
	return nil
}

func runDaemon(args []string, logOut io.Writer) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(logOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	token, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNoToken) {
			return transport.ErrNotPaired
		}
		return err
	}

	// SIGINT / SIGTERM cancel the context, which cascades into the dial loop,
	// the reader goroutine and the ping ticker — Run returns promptly rather
	// than being killed under its feet.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return transport.Loop(ctx, transport.LoopOptions{
		Token:            *token,
		CompanionVersion: version,
		Hostname:         config.Hostname(),
		Log:              log.New(logOut, "", log.LstdFlags),
		// Discover is nil in Step 3: no local-LLM probing yet. Step 4 wires in
		// an Ollama/LM Studio detector and passes it here.
	})
}

func runInstall(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return installService(out)
}

func runUninstall(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(out)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return uninstallService(out)
}

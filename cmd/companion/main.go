// Package main is the Solaigo Companion entry point.
//
// The subcommands are declared here but not yet implemented; this scaffold compiles,
// runs, prints help, and reports its version. Phase 2 wires the subcommands to the
// pair/transport/detect/proxy packages that will live under internal/.
//
// Design: docs/DESIGN.md. Protocol: docs/PROTOCOL.md.
package main

import (
	"fmt"
	"os"
)

// Set by the release workflow via -ldflags "-X main.version=...". The default is
// what someone running `go build` out of a working tree sees.
var version = "0.0.0-dev"

const usage = `solaigo-companion — bridge a local LLM to your Solaigo account

Usage:
  solaigo-companion <command> [arguments]

Commands:
  pair       Enter a pairing code from the cockpit and claim a token
  login      Alias for pair, named for people who expect it
  status     Show the pairing state and the detected local LLM servers
  logout     Revoke this Companion's token locally; the cockpit still shows it
             until you remove it there
  version    Print the version string

Run 'solaigo-companion <command> -h' for subcommand help.

Design and protocol documents live in docs/ in the solaigo-companion repository.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Println(version)
	case "pair", "login":
		notYet("pair")
	case "status":
		notYet("status")
	case "logout":
		notYet("logout")
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// Deliberate stub: the scaffold ships compiled and runnable so the release pipeline
// can be exercised end-to-end before any real code is written. Phase 2 replaces each
// stub with the implementation.
func notYet(cmd string) {
	fmt.Fprintf(os.Stderr, "%s: not implemented yet — this is a Phase 1 scaffold.\n", cmd)
	fmt.Fprintln(os.Stderr, "See docs/DESIGN.md for the planned behaviour.")
	os.Exit(1)
}

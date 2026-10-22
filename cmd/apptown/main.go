// AppTown is the CLI for a apptownd instance — the invocation surface for humans
// and agents alike. Every mutation is attributed via X-AppTown-Actor.
//
// Configuration: APPTOWN_URL (default http://localhost:7777), APPTOWN_USER
// (default $USER).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/clock"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loadCredentials fills APPTOWN_URL / APPTOWN_TOKEN / APPTOWN_USER from
// ~/.config/apptown/credentials (KEY=VALUE lines, 0600) when the
// environment hasn't set them. Environment always wins — the file is
// where a minted token LIVES, not another layer of indirection.
func loadCredentials() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config", "apptown", "credentials"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || os.Getenv(k) != "" {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "APPTOWN_URL":
			api.base = v
		case "APPTOWN_TOKEN":
			_ = os.Setenv("APPTOWN_TOKEN", v)
		case "APPTOWN_USER":
			api.actor = v
		}
	}
}

// now is the CLI's one clock, the real one. Its commands share it, as they
// share the API client (api), rather than each being handed one; a test
// replaces it.
var now = clock.Or(nil)

// root is the CLI's process context: one command, then the process ends,
// and Ctrl-C ends it. A command's waits and git runs start from it.
var root = context.Background()

func main() {
	loadCredentials()
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	c := lookup(os.Args[1])
	if c == nil {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
	if err := c.invoke(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitCode(err))
	}
}

package forge

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/agentenv"
)

// The deterministic gate (guidance_conformance.go, runShChecks): exit 0 is a
// pass, exit 1 is a finding carrying the check's message, any other code is a
// recorded error. These run the REAL sh evaluation against a real tree; the
// only seam is the check executor, redirected from Docker to a local shell so the
// exit-code→verdict mapping can be exercised at DEFAULT tier (no daemon).

// tgLocalSh points the conformance executor at the local shell for the test.
func tgLocalSh(t *testing.T, a *App) {
	t.Helper()
	installCheckEffects(t, a, checkOverrides{run: func(req ExecRequest) ExecOutcome {
		return agentenv.Execute(a.processRunner, context.Background(), ExecutorLocal, req, time.Now())
	}})
}

// stockChecksOf loads a shipped block's checks, so a test runs the very
// script the fleet will.

// repo/no-build-artifacts asks git which committed files the tree's own
// ignore rules cover: the forge keeps no list of build folder names, the
// project's .gitignore is the declaration. The staged tree
// the check sees is a git archive — exactly the tracked files, no .git —
// so a file that is present AND ignored is one that was force-added.

// no-build-artifacts-tracked names a committed compiled program by its
// format signature, whatever its size or name; large data and media are
// not its business, and neither is a list of extensions.

// Each lane's block asks git whether a file under its tool's output folder
// would be ignored, whatever spelling the project used: the folder names
// are the lane's knowledge, the judgment is git's.

// build/container-image: the final stage's USER decides — a build stage may
// stay root, the runtime stage may not; no Dockerfile means nothing to hold.

// Every `tool` check a stock block declares names a stock scanner binding,
// and every way-of-working tool is manual (never fanned out at a scan point).

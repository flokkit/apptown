package apptown

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/gitexec/gittest"

	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// The baseline (guidance_baseline.go, design/subsystems/ways-of-working-versions.md
// §4.2): a check that reports occurrences is judged by them — what was
// already on the base is preexisting, what is new is the change's; a
// boolean check failing on both trees is undecided and goes to the
// reviewer; a remediation's own target is judged absolutely.
func TestBaselineJudgesOccurrencesAndLeavesBooleansUndecided(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	tgLocalSh(t, a)
	pid, _, _ := fleetRepo(t, a, "rl", map[string]string{
		"go.mod":  "module rl\n\nrequire modernc.org/sqlite v1.0.0\n",
		"main.go": "package main // FIXME someday\n",
	})
	adopt(t, a, pid, "data/networked", 1, `[
		{"appliesTo":["repository"],"name":"no-file-database","evaluator":"sh","occurrences":"lines","sh":"bad=$(grep -Hn sqlite go.mod); [ -z \"$bad\" ] || { echo \"$bad\"; exit 1; }","severity":"high","expects":"no sqlite"},
		{"appliesTo":["repository"],"name":"no-fixme","evaluator":"sh","sh":"grep -rq FIXME . && { echo 'FIXME left'; exit 1; }; exit 0","severity":"high","expects":"no FIXME"}]`)
	ctx := a.asEngine(context.Background())
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Title: "tidy"})
	if err != nil {
		t.Fatal(err)
	}
	ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(change.ID)
	wsGit := func(args ...string) {
		c := gittest.Command(args...)
		c.Dir = ws
		c.Env = append(c.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	commit := func(name, body, msg string) {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		wsGit("add", "-A")
		wsGit("commit", "-q", "-m", msg)
	}
	check := func() map[string]any {
		ch, err := a.changeByID(ctx, change.ID)
		if err != nil {
			t.Fatal(err)
		}
		out, err := a.verification.Check(testConformanceContext("t"), ch, verification.Selection{})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	rowOf := func(out map[string]any, name string) workflow.CheckReceipt {
		for _, r := range out["checks"].(verification.Results) {
			if r.Check == name {
				return r
			}
		}
		t.Fatalf("no result for %s", name)
		return workflow.CheckReceipt{}
	}

	// A change that touches neither: the sqlite line is preexisting (every
	// occurrence was on the base); the FIXME check is boolean → undecided.
	commit("README.md", "hello\n", "add a readme")
	out := check()
	if out["failing"] != 0 || out["preexisting"] != 1 || out["undecided"] != 1 || out["ok"] != true {
		t.Fatalf("nothing new: %+v", out)
	}
	if r := rowOf(out, "no-file-database"); r.Preexisting != true {
		t.Fatalf("sqlite is preexisting: %+v", r)
	}
	if r := rowOf(out, "no-fixme"); r.Undecided != true || !strings.Contains(r.Note, "reviewer judges") {
		t.Fatalf("the boolean check is undecided, for the reviewer: %+v", r)
	}
	if !strings.Contains(out["note"].(string), "undecided") || !strings.Contains(out["note"].(string), "preexisting") {
		t.Fatalf("the agent is told both: %+v", out["note"])
	}

	// A change that adds a SECOND sqlite driver: the new occurrence is the
	// change's and counts; the old one is still not.
	commit("go.mod", "module rl\n\nrequire modernc.org/sqlite v1.0.0\nrequire github.com/mattn/go-sqlite3 v1.14.0\n", "add another driver")
	out = check()
	if out["failing"] != 1 || out["preexisting"] != 0 || out["ok"] != false {
		t.Fatalf("a second driver is new: %+v", out)
	}
	r := rowOf(out, "no-file-database")
	if r.Preexisting == true {
		t.Fatalf("not preexisting when something is new: %+v", r)
	}

	// A remediation opened for that very check is judged absolutely.
	change, err = a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: pid, Type: "remediation", Title: "Conform to data/networked@1 · no-fixme", Acceptance: &workflow.Acceptance{Checks: []workflow.AcceptanceCheck{{Block: "data/networked", Version: 1, Check: "no-fixme"}}}})
	if err != nil {
		t.Fatal(err)
	}
	ws = (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(change.ID)
	commit("README.md", "hello\n", "add a readme")
	commit("go.mod", "module rl\n\nrequire modernc.org/sqlite v1.0.0\nrequire github.com/mattn/go-sqlite3 v1.14.0\n", "add another driver")
	out = check()
	if out["undecided"] != 0 || out["failing"] != 2 {
		t.Fatalf("the remediation's own target is never undecided: %+v", out)
	}
}

// §4.2 hardened: a landing is judged against current main, not the fork
// point; provenance says whether a base is on main; the artifact's findings
// carry how each read against the baseline.

// A remediation may not move the dependencies: the lockfiles it touched are
// named as out of scope, unless the check it closes is about dependencies
// or the toolchain. It may ADD a package the fix needs: a dependency file
// that only gained lines is in scope.

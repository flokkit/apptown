package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"

	"flokkit.io/forge/internal/forge/workflow"
)

type noBriefModel struct{}

func (noBriefModel) Resolve(context.Context, ids.ID, string) (string, string, string) {
	return "", "", ""
}
func TestBuildApprovalBriefSkeleton(t *testing.T) {
	t.Parallel()
	h := ApprovalBriefs{selection: noBriefModel{}}
	approval := &workflow.Step{ID: idstest.N(4), Name: "accept", Type: "approval", Status: workflow.StatusPending,
		Config: json.RawMessage(`{"means":"you are accepting the visual design","instructions":"look at the screenshots"}`)}
	brief := h.buildApprovalBrief(context.Background(), workflow.ApprovalBriefInputs{
		StepContext: workflow.StepContext{Step: approval, Run: &workflow.Run{ProjectID: idstest.N(1)}},
		Change:      &workflow.Change{Title: "Add dark mode", Body: "A toggle in settings.", Summary: "Implemented the toggle and wired the theme."},
		Steps: []*workflow.Step{
			{ID: idstest.N(1), Name: "build", Type: "build", Status: workflow.StatusPass},
			{ID: idstest.N(2), Name: "review", Type: "agent/review", Status: workflow.StatusPass, Evidence: json.RawMessage(`{"summary":"reads cleanly, no concerns"}`)},
			{ID: idstest.N(3), Name: "scan", Type: "scan", Status: workflow.StatusPass, OverrideBy: "bls", OverrideReason: "known false positive"},
			{ID: idstest.N(5), Name: "second-review", Type: "agent/review", Status: workflow.StatusPass, OverrideBy: "ana", OverrideReason: "the reviewer misread the spec", Evidence: json.RawMessage(`{"verdict":"fail","summary":"the toggle is missing"}`)},
			approval,
		},
		Report: "I added the toggle and tested both themes.",
		Asked:  &workflow.PersonFeedback{Note: "make the toggle remember the choice", By: "ana"},
	}, " settings.tsx | 12 ++++++++----\n 1 file changed, 8 insertions(+), 4 deletions(-)")

	for _, want := range []string{
		"THE ASK: Add dark mode",
		"A toggle in settings.",
		"WHAT WAS ASKED SINCE (a person sent the work back; this is part of the ask):\nmake the toggle remember the choice (— ana)",
		"STORY SO FAR:",
		"Implemented the toggle and wired the theme.",
		"MACHINES ALREADY VERIFIED:",
		"reads cleanly, no concerns",                             // the review summary
		"- scan (scan): OVERRIDDEN by bls: known false positive", // the overridden scan
		// An overridden review is an override first, never a pass with a summary.
		"- second-review (agent/review): OVERRIDDEN by ana: the reviewer misread the spec (the check itself did not pass) — the toggle is missing",
		"THE DIFF (the change's whole branch against its base):\nsettings.tsx | 12 ++++++++----", // what is accepted
		"IMPLEMENTER'S REPORT",
		"I added the toggle and tested both themes.",
		"WHAT YOUR OK MEANS HERE: you are accepting the visual design",
		"GATE INSTRUCTIONS: look at the screenshots",
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief missing %q:\n%s", want, brief)
		}
	}
	// The approval step itself must not be listed among the machine checks.
	if strings.Contains(brief, "- accept (approval)") {
		t.Fatalf("the gate step should not list itself:\n%s", brief)
	}
}

// The brief tells the approver what they accept: the change's committed
// diff, its base to its branch, file by file, without what it merged from
// main. A diff that cannot be read says so;
// it is never shown as no changes.
func TestTheApprovalBriefCarriesTheChangesDiffStat(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	dataDir := t.TempDir()
	hub := filepath.Join(dataDir, "workspaces", "project-shop")
	if err := os.MkdirAll(hub, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(args...)
		cmd.Dir = hub
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(hub, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	write("README.md", "hello\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-q", "-b", "forge/change-00000000-0001-7000-8000-000000000000")
	write("settings.tsx", "export const dark = true\n")
	git("add", "-A")
	git("commit", "-q", "-m", "the change")
	run, err := workflow.ReadRunIn(f.Context, f.DB, f.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Projects.SetDefaultBranch(context.Background(), f.ProjectID, "main"); err != nil {
		t.Fatal(err)
	}
	h := ApprovalBriefs{db: f.DB, dataDir: dataDir}
	inputs := workflow.ApprovalBriefInputs{StepContext: workflow.StepContext{Run: run}, Change: &workflow.Change{ID: f.ChangeID, Branch: "forge/change-00000000-0001-7000-8000-000000000000", BaseSHA: base}}
	if stat := h.diffStat(f.Context, inputs); !strings.Contains(stat, "settings.tsx | 1 +") || !strings.Contains(stat, "1 file changed") {
		t.Fatalf("the diff stat: %q", stat)
	}
	// Main moves and the change catches up with it: main's file is not the
	// change's, and the brief must not ask the approver to accept it.
	git("checkout", "-q", "main")
	write("main_test.go", "package main\n")
	git("add", "-A")
	git("commit", "-q", "-m", "on main")
	git("checkout", "-q", "forge/change-00000000-0001-7000-8000-000000000000")
	git("merge", "-q", "--no-edit", "-m", "Merge main into forge/change-00000000-0001-7000-8000-000000000000", "main")
	if stat := h.diffStat(f.Context, inputs); strings.Contains(stat, "main_test.go") || !strings.Contains(stat, "1 file changed") {
		t.Fatalf("after catching up with main, the diff stat: %q", stat)
	}
	inputs.Change.Branch = "forge/change-gone"
	if stat := h.diffStat(f.Context, inputs); !strings.HasPrefix(stat, "could not be read: ") {
		t.Fatalf("an unreadable diff: %q", stat)
	}
}

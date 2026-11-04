package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

func TestTheApprovalBriefCarriesTheChangesDiffStat(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	f := newWorkflowHTTPFixtureAt(t, dataDir)
	hub := filepath.Join(dataDir, "workspaces", "project-"+f.ProjectID.String())
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
	git("checkout", "-q", "-b", "apptown/change-00000000-0001-7000-8000-000000000000")
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
	h := f.Work
	inputs := workflow.ApprovalBriefInputs{StepContext: workflow.StepContext{Run: run}, Change: &workflow.Change{ID: f.ChangeID, Branch: "apptown/change-00000000-0001-7000-8000-000000000000", BaseSHA: base}}
	if stat := h.BriefDiff(f.Context, inputs); !strings.Contains(stat, "settings.tsx | 1 +") || !strings.Contains(stat, "1 file changed") {
		t.Fatalf("the diff stat: %q", stat)
	}
	// Main moves and the change catches up with it: main's file is not the
	// change's, and the brief must not ask the approver to accept it.
	git("checkout", "-q", "main")
	write("main_test.go", "package main\n")
	git("add", "-A")
	git("commit", "-q", "-m", "on main")
	git("checkout", "-q", "apptown/change-00000000-0001-7000-8000-000000000000")
	git("merge", "-q", "--no-edit", "-m", "Merge main into apptown/change-00000000-0001-7000-8000-000000000000", "main")
	if stat := h.BriefDiff(f.Context, inputs); strings.Contains(stat, "main_test.go") || !strings.Contains(stat, "1 file changed") {
		t.Fatalf("after catching up with main, the diff stat: %q", stat)
	}
	inputs.Change.Branch = "apptown/change-gone"
	if stat := h.BriefDiff(f.Context, inputs); !strings.HasPrefix(stat, "could not be read: ") {
		t.Fatalf("an unreadable diff: %q", stat)
	}
}

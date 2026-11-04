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

func TestTheApprovalBriefCarriesTheChangesFilesAndPatch(t *testing.T) {
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
	if files, patch := h.BriefDiff(f.Context, inputs); !strings.Contains(files, "settings.tsx | 1 +") || !strings.Contains(files, "1 file changed") || !strings.Contains(patch, "+export const dark = true") {
		t.Fatalf("the diff: %q %q", files, patch)
	}
	// Main moves and the change catches up with it: main's file is not the
	// change's, and the brief must not ask the approver to accept it.
	git("checkout", "-q", "main")
	write("main_test.go", "package main\n")
	git("add", "-A")
	git("commit", "-q", "-m", "on main")
	git("checkout", "-q", "apptown/change-00000000-0001-7000-8000-000000000000")
	git("merge", "-q", "--no-edit", "-m", "Merge main into apptown/change-00000000-0001-7000-8000-000000000000", "main")
	if files, patch := h.BriefDiff(f.Context, inputs); strings.Contains(files, "main_test.go") || !strings.Contains(files, "1 file changed") || !strings.Contains(patch, "+export const dark = true") {
		t.Fatalf("after catching up with main, the diff: %q %q", files, patch)
	}
	write("large.txt", strings.Repeat("water\n", 4000))
	git("add", "-A")
	git("commit", "-q", "-m", "large patch")
	files, patch := h.BriefDiff(f.Context, inputs)
	if len(patch) > 16100 || !strings.Contains(patch, "[Patch truncated at 16000 bytes.]") || !strings.Contains(files, "settings.tsx") {
		t.Fatalf("unbounded or silent truncation: %d bytes, files %q", len(patch), files)
	}
	inputs.Change.Branch = "apptown/change-gone"
	if files, patch := h.BriefDiff(f.Context, inputs); !strings.HasPrefix(files, "could not be read: ") {
		t.Fatalf("an unreadable diff: %q %q", files, patch)
	}
}

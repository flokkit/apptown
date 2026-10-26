package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func routineGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test"}, args...)
	out, err := gittest.Command(argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func routineWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
func routineRepository(t *testing.T) (string, string, workflow.Source) {
	t.Helper()
	data := t.TempDir()
	source := workflow.Source{ProjectID: idstest.N(1), ChangeID: idstest.N(2), RunID: idstest.N(3), RootRunID: idstest.N(3), Branch: "change", DefaultBranch: "main", Slug: "example"}
	repo, err := source.Repository(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	routineGit(t, repo, "init", "-b", "main")
	for _, version := range []string{"1.0.0", "1.0.1"} {
		s := routineFixture(version)
		routineWrite(t, filepath.Join(repo, "package.json"), s.Manifest)
		routineWrite(t, filepath.Join(repo, "package-lock.json"), s.Lock)
		routineGit(t, repo, "add", ".")
		routineGit(t, repo, "commit", "-m", version)
		if version == "1.0.0" {
			source.ChangeBaseSHA = routineGit(t, repo, "rev-parse", "HEAD")
			routineGit(t, repo, "checkout", "-b", "change")
		}
	}
	return data, repo, source
}
func routineExecutor(t *testing.T, after func()) Executor {
	t.Helper()
	return Executor{Local: true, AgentImage: "test-agent", Image: func(context.Context, execution.ImageRequest) execution.ImageAnswer {
		return execution.ImageAnswer{ID: "sha256:" + strings.Repeat("a", 64)}
	}, Run: func(context.Context, execution.Request) execution.Outcome {
		report := routineReport(routineFixture("1.0.0"), routineFixture("1.0.1"))
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if after != nil {
			after()
		}
		return execution.Outcome{Termination: execution.Exited, ExitCode: 0, Report: raw}
	}}
}
func TestRoutineInspectionUsesTheCompleteDiffAndCaughtUpBase(t *testing.T) {
	data, repo, source := routineRepository(t)
	routineGit(t, repo, "checkout", "main")
	routineWrite(t, filepath.Join(repo, "README"), []byte("new main content\n"))
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "main progressed")
	main := routineGit(t, repo, "rev-parse", "HEAD")
	routineGit(t, repo, "checkout", "change")
	routineGit(t, repo, "merge", "main", "--no-edit")
	inspector := NewRoutineInspector(data, &scm.RepositoryLocks{}, routineExecutor(t, nil))
	result, err := inspector.InspectRoutine(t.Context(), source, "patch")
	if err != nil || result.Classification != "routine-dependency" || result.Base != main || len(result.ChangedFiles) != 2 {
		t.Fatalf("wrong caught-up source: %+v %v", result, err)
	}
	routineWrite(t, filepath.Join(repo, "unrelated.txt"), []byte("code change\n"))
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "mixed change")
	inspector.executor.Run = func(context.Context, execution.Request) execution.Outcome {
		t.Fatal("mixed source executed validator")
		return execution.Outcome{}
	}
	result, err = inspector.InspectRoutine(t.Context(), source, "patch")
	if err != nil || result.Classification != "requires-person" || len(result.ChangedFiles) != 3 {
		t.Fatalf("mixed diff concealed: %+v %v", result, err)
	}
}
func TestRoutineInspectionRefusesSourceMovementAndDirtyWorkspace(t *testing.T) {
	data, repo, source := routineRepository(t)
	inspector := NewRoutineInspector(data, &scm.RepositoryLocks{}, routineExecutor(t, func() {
		routineWrite(t, filepath.Join(repo, "moved.txt"), []byte("new work"))
		routineGit(t, repo, "add", ".")
		routineGit(t, repo, "commit", "-m", "moved")
	}))
	result, err := inspector.InspectRoutine(t.Context(), source, "patch")
	if err != nil || result.Classification != "cannot-assess" || !strings.Contains(result.Reason, "moved") {
		t.Fatalf("moved source qualified: %+v %v", result, err)
	}
	// Return to the published pure candidate, then create its own local checkout.
	routineGit(t, repo, "reset", "--hard", "HEAD~1")
	workspace, err := source.Workspace(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err = (workflow.GitWorkspace{DataDir: data}).EnsureClone(t.Context(), workflow.CloneRequest{Repository: repo, Workspace: workspace, Branch: source.Branch, DefaultBranch: source.DefaultBranch}); err != nil {
		t.Fatal(err)
	}
	routineWrite(t, filepath.Join(workspace, "dirty.txt"), []byte("unsaved work"))
	inspector.executor.Run = func(context.Context, execution.Request) execution.Outcome {
		t.Fatal("dirty source executed validator")
		return execution.Outcome{}
	}
	result, err = inspector.InspectRoutine(t.Context(), source, "patch")
	if err != nil || result.Classification != "cannot-assess" || !strings.Contains(result.Reason, "dirty") {
		t.Fatalf("dirty source qualified: %+v %v", result, err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "dirty.txt"))
	if err != nil || string(raw) != "unsaved work" {
		t.Fatalf("inspection modified source: %q %v", raw, err)
	}
}

func TestRoutineRefusesUnchangedShrinkwrap(t *testing.T) {
	data, repo, source := routineRepository(t)
	routineGit(t, repo, "checkout", "main")
	routineWrite(t, filepath.Join(repo, "npm-shrinkwrap.json"), routineFixture("1.0.0").Lock)
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "existing shrinkwrap")
	routineGit(t, repo, "checkout", "change")
	routineGit(t, repo, "merge", "main", "--no-edit")
	calls := 0
	inspector := NewRoutineInspector(data, &scm.RepositoryLocks{}, routineExecutor(t, func() { calls++ }))
	result, err := inspector.InspectRoutine(t.Context(), source, "patch")
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification != "requires-person" || calls != 0 {
		t.Fatalf("unsupported shrinkwrap must refuse before validation: classification=%s calls=%d", result.Classification, calls)
	}
}

func TestRoutineReportsAllMixedFiles(t *testing.T) {
	data, repo, source := routineRepository(t)
	routineWrite(t, filepath.Join(repo, "README"), []byte("mixed edit"))
	routineGit(t, repo, "add", ".")
	routineGit(t, repo, "commit", "-m", "mixed source")
	inspector := NewRoutineInspector(data, &scm.RepositoryLocks{}, routineExecutor(t, nil))
	result, err := inspector.InspectRoutine(t.Context(), source, "patch")
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification != "requires-person" || len(result.ChangedFiles) != 3 {
		t.Fatalf("incomplete mixed inventory: classification=%s files=%v", result.Classification, result.ChangedFiles)
	}
}

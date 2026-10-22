package apptown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
)

// These checks exercise step 1's commit-only rule through an admitted working run.
func step1ReviewGuidance(t *testing.T, repository bool, checks string) (*App, *workflow.Change) {
	t.Helper()
	a := testApp(t)
	pid := plainTestProject(t, a.DB, "step1-review", "Step 1 Review")
	p := a.projectByID(context.Background(), pid)
	if repository {
		if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
			t.Fatal(err)
		}
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "review", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "guidance", Uses: "guidance/conformance"}}}}}})
	if err := a.lifecycle.SelectLane(t.Context(), p.ID, "review", true, "review"); err != nil {
		t.Fatal(err)
	}
	adopt(t, a, pid, "step1-review", 1, checks)
	ch := engineChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	return a, ch
}

func step1ReviewRun(t *testing.T, a *App, ch *workflow.Change) *workflow.Run {
	t.Helper()
	a.Work.Stop()
	run, err := a.Work.StartRun(a.asAppTown(context.Background(), ch.ProjectID, "review"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "review"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	return run
}

func TestGuidanceChecksRefuseAFolderWithoutARepository(t *testing.T) {
	t.Parallel()
	a, ch := step1ReviewGuidance(t, false, `[{"name":"clean","evaluator":"sh","sh":"true"}]`)
	var folders []string
	installCheckEffects(t, a, checkOverrides{run: func(req ExecRequest) ExecOutcome {
		folders = append(folders, req.Workspace)
		return ExecOutcome{}
	}})
	run := step1ReviewRun(t, a, ch)
	result := reviewExecute(t, a, run, "guidance")
	if result.Status != workflow.StatusError || len(folders) != 0 {
		t.Fatalf("repository-less check executed: status=%s folders=%v evidence=%s", result.Status, folders, result.Evidence)
	}
}

func TestGuidanceChecksKeepImageAndSourceOnOneCommit(t *testing.T) {
	t.Parallel()
	a, ch := step1ReviewGuidance(t, true, `[{"name":"source","evaluator":"sh","sh":"true"},{"name":"image","evaluator":"tool","tool":"step1-image","severity":"high"}]`)
	attachScanner(t, a, "step1-image", map[string]any{"command": []string{"true"}, "target": "exec-image", "manual": true})
	ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(ch.ID)
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte("app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "source.txt"), []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "-f", ".apptown/app.yaml", "source.txt")
	gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "first")
	daemon := dockerfake.New()
	a.docker.Client = daemon
	moved := false
	installCheckEffects(t, a, checkOverrides{run: func(req ExecRequest) ExecOutcome {
		if !moved {
			body, err := os.ReadFile(filepath.Join(req.Workspace, "source.txt"))
			if err != nil || string(body) != "first" {
				t.Fatalf("staged source: %s %v", body, err)
			}
			moved = true
			if err := os.WriteFile(filepath.Join(ws, "source.txt"), []byte("second"), 0644); err != nil {
				t.Fatal(err)
			}
			gittest.Run(t, ws, "add", "source.txt")
			gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "concurrent commit")
		}
		return ExecOutcome{}
	}})
	run := step1ReviewRun(t, a, ch)
	checked := gittest.Run(t, ws, "rev-parse", "HEAD")
	result := reviewExecute(t, a, run, "guidance")
	calls := fmt.Sprint(daemon.Matching("image inspect "))
	if !moved || !strings.Contains(calls, "apptown-conform:"+checked[:12]) {
		t.Fatalf("source was %s but image lookup was %q; current HEAD=%s status=%s evidence=%s", checked, calls, gittest.Run(t, ws, "rev-parse", "HEAD"), result.Status, result.Evidence)
	}
}

// commitFolder makes a run folder's files a commit, as a check reads one: a
// repository-less scenario's folder gets a repository first.
func commitFolder(t *testing.T, ws string) {
	t.Helper()
	if ws == "" {
		t.Fatal("commitFolder needs a folder")
	}
	if _, err := os.Stat(filepath.Join(ws, ".git")); err != nil {
		gittest.Run(t, ws, "init", "-q")
	}
	gittest.Run(t, ws, "add", "-A", "-f")
	gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-q", "--allow-empty", "-m", "the run's work")
}

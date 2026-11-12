package apptown

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/stock"
	yaml "gopkg.in/yaml.v3"
)

// The provenance base is the branch's fork point from main — not the tip the
// landing run started at (which is the change's own head).
func TestLandingSourceRetainsTheForkPoint(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	pid, fork, git := fleetRepo(t, a, "fp", map[string]string{"a.txt": "one"})
	p := a.projectByID(context.Background(), pid)
	if p == nil {
		t.Fatal("project missing")
	}
	if _, err := a.recordAdoption(p, "edit", "fork-point fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	ch := engineLandingChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	branch := ch.Branch
	def := strings.TrimSpace(git("rev-parse", "--abbrev-ref", "HEAD"))
	git("checkout", "-q", "-b", branch)
	writeRepoFile(t, a, "fp", "b.txt", "two")
	git("add", "-A")
	git("commit", "-q", "-m", "change commit 1")
	tip1 := strings.TrimSpace(git("rev-parse", "HEAD"))
	writeRepoFile(t, a, "fp", "c.txt", "three")
	git("add", "-A")
	git("commit", "-q", "-m", "change commit 2")
	git("checkout", "-q", def)
	git("commit", "-q", "--allow-empty", "-m", "main moved on")

	// Admit the landing run through Work; provenance must still use the branch
	// fork point rather than the run's current branch tip.
	installFakeGit(t, a, func(in workflow.CloneRequest) (workflow.WorkspaceResult, error) {
		return workflow.WorkspaceResult{Path: in.Workspace, Tip: tip1}, nil
	})
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "land"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	source := workflowSourceForTest(t, a, run)
	if source.RunBaseSHA != tip1 {
		t.Fatalf("landing run must retain branch tip %s as its run base, got %q", tip1[:8], source.RunBaseSHA)
	}
	if source.ChangeBaseSHA != fork {
		t.Fatalf("change base must retain the fork point %s, not the run base %s: got %s", fork, tip1, source.ChangeBaseSHA)
	}
}

func TestAScannersEmptyExitRemainsUnableToJudgeInTheScanRead(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	body, err := stock.FS.ReadFile("scanners.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var declared struct {
		Scanners []struct {
			Name      string `yaml:"name"`
			EmptyExit int    `yaml:"emptyExit"`
		} `yaml:"scanners"`
	}
	if err := yaml.Unmarshal(body, &declared); err != nil {
		t.Fatal(err)
	}
	empty := 0
	for _, scanner := range declared.Scanners {
		if scanner.Name == "osv" {
			empty = scanner.EmptyExit
		}
	}
	if empty == 0 {
		t.Fatal("OSV must declare its nothing-to-judge exit")
	}
	attachScanner(t, a, "osv", map[string]any{"image": "osv:test", "command": []string{"scan"}, "target": "source", "emptyExit": empty})
	run, source := reviewLanding(t, a, []schema.Step{{Name: "source", Uses: "scan", With: map[string]any{"scanner": "osv", "baseline": "none"}}, {Name: "peer", Uses: "scan", With: map[string]any{"scanner": "gitleaks", "baseline": "none"}}}, map[string]string{"work.txt": "change"}, nil)
	calls := 0
	a.scanSteps = verification.NewScanSteps(verification.ScanDependencies{DB: a.DB, Scanners: a.scanners, Conformance: a.conformance, Integrations: a.Integrations, Progress: &a.progress, DataDir: a.cfg.dataDir, Executor: verification.Executor{Run: func(context.Context, execution.Request) execution.Outcome {
		calls++
		return execution.Outcome{Termination: execution.Exited, ExitCode: empty}
	}}})
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	a.Work.Stop()
	result := reviewExecute(t, a, run, "source")
	if result.Status != workflow.StatusError || calls != 1 {
		t.Fatalf("nothing-to-judge settlement: %s calls=%d evidence=%s", result.Status, calls, result.Evidence)
	}
	observations, err := a.Work.LatestMainScans(a.asAppTown(t.Context(), run.ProjectID, "fixture"), run.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	all := append(observations.Source, observations.Historical...)
	if len(all) != 1 || !slices.Equal(all[0].ExpectedScanners, []string{"gitleaks", "osv"}) {
		t.Fatalf("scan did not retain its expected bindings: %+v", all)
	}
	// Make the scanned commit the current source subject in this disposable repository.
	repo := requireLocation(t)(source.Repository(a.cfg.dataDir))
	gittest.Run(t, repo, "update-ref", "refs/heads/"+source.DefaultBranch, source.Landing.Candidate)
	got, err := a.Work.LatestMainScans(a.asAppTown(t.Context(), run.ProjectID, "fixture"), run.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Known || len(got.Source) != 1 || got.Source[0].Verdict != "error" || !strings.Contains(got.Source[0].Reason, "found nothing to judge") {
		t.Fatalf("nothing-to-judge read: %+v", got)
	}
}

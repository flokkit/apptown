package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/execfile"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// A landing's secret scan checks every commit the landing brings to the
// default branch, not only its final tree: a scanner that declares a
// history command is given the project's repository, read-only, and the
// range from the target the landing was checked against to its candidate.
// A secret committed and then deleted is in that range though not in the
// tree: the stock gitleaks command's history mode finds such a secret, its
// tree mode does not.
func TestALandingsSecretScanChecksEveryCommitItAdds(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	// The Docker the scan runs through: it records the command and writes
	// the scanner's report with one finding, as gitleaks does.
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "docker")
	script := `#!/bin/sh
echo "$@" >> "` + calls + `"
case "$*" in inspect*OOMKilled*) echo false; exit 0;; "rm -f "*) exit 0;; esac
work=""
prev=""
for arg in "$@"; do
  case "$arg" in *:/work) [ "$prev" = "-v" ] && work="${arg%:/work}";; esac
  prev="$arg"
done
mkdir -p "$work/.forge"
cat > "$work/.forge/secrets.sarif" <<'EOF'
{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"gitleaks","rules":[{"id":"github-pat"}]}},"results":[{"ruleId":"github-pat","level":"error","message":{"text":"a GitHub token"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"cfg.js"},"region":{"startLine":1}}}]}]}]}
EOF
exit 1
`
	if err := execfile.Write(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	attachScanner(t, a, "secrets", map[string]any{
		"image": "gitleaks:test", "command": []string{"detect", "--source", "/work", "--no-git"},
		"historyCommand": []string{"git", "/history", "--log-opts", "${HISTORY_RANGE}"},
		"report":         ".forge/secrets.sarif", "format": "sarif", "kind": "secret", "target": "source",
	})
	run, source := reviewLanding(t, a, []schema.Step{{Name: "secrets", Uses: "scan", With: map[string]any{"scanner": "secrets"}}}, map[string]string{"work.txt": "the change\n"}, nil)
	a.processRunner = execution.Runner{DockerBinary: fake}
	a.scanSteps = scanStepsFor(a)
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	a.Work.Stop()

	scan := reviewExecute(t, a, run, "secrets")
	if scan.Status != workflow.StatusFail {
		t.Fatalf("the scan's finding did not fail the gate: %s %s", scan.Status, scan.Evidence)
	}
	recorded, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	commits := source.Landing.Target + ".." + source.Landing.Candidate
	repository := source.Repository(a.cfg.dataDir) + ":/history:ro"
	if got := string(recorded); !strings.Contains(got, "git /history --log-opts "+commits) || !strings.Contains(got, repository) || strings.Contains(got, "--no-git") {
		t.Fatalf("the landing's scan did not read the commits it adds (%s, %s):\n%s", commits, repository, got)
	}
}

// A scan outside a landing reads its folder's committed HEAD, resolved once,
// named in its evidence and staged outside the folder; a file the run left
// uncommitted is never scanned.
func TestAScanOutsideALandingReadsTheCommittedTree(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "docker")
	script := `#!/bin/sh
case "$*" in inspect*OOMKilled*) echo false; exit 0;; "rm -f "*) exit 0;; esac
prev=""
for arg in "$@"; do
  case "$arg" in *:/work) [ "$prev" = "-v" ] && work="${arg%:/work}";; esac
  prev="$arg"
done
echo "$work" >> "` + calls + `"
ls "$work" >> "` + calls + `"
exit 0
`
	if err := execfile.Write(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	attachScanner(t, a, "secrets", map[string]any{
		"image": "gitleaks:test", "command": []string{"detect", "--source", "/work", "--no-git"},
		"kind": "secret", "target": "source",
	})
	pid := plainTestProject(t, a.DB, "scan-unpinned", "Scan Unpinned")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "review", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{
		{Name: "gates", Steps: []schema.Step{{Name: "secrets", Uses: "scan", With: map[string]any{"scanner": "secrets"}}}},
	}}})
	if err := a.lifecycle.SelectLane(t.Context(), p.ID, "review", true, "review"); err != nil {
		t.Fatal(err)
	}
	ch := engineChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(a.cfg.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	if err := os.WriteFile(filepath.Join(ws, "committed.txt"), []byte("in the commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "committed.txt")
	gittest.Run(t, ws, "-c", "user.name=t", "-c", "user.email=t@example.org", "commit", "-qm", "the change's work")
	a.processRunner = execution.Runner{DockerBinary: fake}
	a.scanSteps = scanStepsFor(a)
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "review"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	// Written after the run started (which commits a folder's leftovers).
	head := gittest.Run(t, ws, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(ws, "uncommitted.txt"), []byte("not committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scan := reviewExecute(t, a, run, "secrets")
	if scan.Status != workflow.StatusPass || !strings.Contains(string(scan.Evidence), head) {
		t.Fatalf("the scan does not name the commit it read (%s): %s %s", head, scan.Status, scan.Evidence)
	}
	recorded, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(recorded)
	if strings.HasPrefix(got, ws) || !strings.Contains(got, "committed.txt") || strings.Contains(got, "uncommitted.txt") {
		t.Fatalf("the scan did not read the committed tree staged outside the folder:\n%s", got)
	}
}

// With a worker configured, a scan runs on the worker and never on this
// machine: the forge's own Docker, which fails any run, is not called.
func TestAScanRunsOnTheWorkerWhenOneIsConfigured(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	dir := t.TempDir()
	local := filepath.Join(dir, "local-docker")
	calls := filepath.Join(dir, "calls")
	if err := execfile.Write(local, []byte("#!/bin/sh\necho local \"$@\" >> "+calls+"\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	remote := &countedChecks{}
	ca := workertest.NewCA(t)
	srv := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Checks: remote})
	a.cfg.worker = &execution.WorkerTasks{URL: srv.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	a.processRunner = execution.Runner{DockerBinary: local}
	attachScanner(t, a, "secrets", map[string]any{
		"image": "gitleaks:test", "command": []string{"detect", "--source", "/work"}, "kind": "secret", "target": "source",
	})
	a.scanSteps = scanStepsFor(a)
	pid := plainTestProject(t, a.DB, "scan-on-worker", "Scan On Worker")
	p := a.projectByID(context.Background(), pid)
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "main", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "review", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{
		{Name: "gates", Steps: []schema.Step{{Name: "secrets", Uses: "scan", With: map[string]any{"scanner": "secrets"}}}},
	}}})
	if err := a.lifecycle.SelectLane(t.Context(), p.ID, "review", true, "review"); err != nil {
		t.Fatal(err)
	}
	ch := engineChange(t, a, p)
	if err := prepareChangeWorkspaceForTest(a, p, ch); err != nil {
		t.Fatal(err)
	}
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	a.Work.Stop()
	run, err := a.Work.StartRun(asLander(t, a), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "review"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	scan := reviewExecute(t, a, run, "secrets")
	recorded, _ := os.ReadFile(calls)
	if scan.Status != workflow.StatusPass || strings.Contains(string(recorded), "local ") || remote.runs.Load() == 0 {
		t.Fatalf("the scan did not run on the worker alone: %s %s\n%s", scan.Status, scan.Evidence, recorded)
	}
}

// countedChecks is a worker's check runner that counts its runs and answers
// each with a clean exit.
type countedChecks struct{ runs atomic.Int64 }

func (c *countedChecks) Run(context.Context, string, execution.Request) execution.Outcome {
	c.runs.Add(1)
	return execution.Outcome{Termination: execution.Exited}
}

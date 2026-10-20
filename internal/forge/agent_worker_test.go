package forge

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
	"flokkit.io/forge/internal/worker/workertest"
)

// workerClientConfig is AppTown's side of the test worker at url: a client
// certificate for the installation, its key and the CA, written as files.
func workerClientConfig(t *testing.T, ca *workertest.CA, url string) *WorkerConfig {
	t.Helper()
	dir := t.TempDir()
	caFile, _, _ := ca.ServerFiles(t, dir)
	client := ca.Issue(t, workertest.Installation, x509.ExtKeyUsageClientAuth)
	key, err := x509.MarshalPKCS8PrivateKey(client.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "client.pem"), filepath.Join(dir, "client-key.pem")
	for path, block := range map[string]*pem.Block{certFile: {Type: "CERTIFICATE", Bytes: client.Certificate[0]}, keyFile: {Type: "PRIVATE KEY", Bytes: key}} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &WorkerConfig{URL: url, Cert: certFile, Key: keyFile, CA: caFile}
}

// localTripwire is AppTown's own agent process on an AppTown with a worker:
// any call to it fails the test, since nothing may run on this machine.
type localTripwire struct{ t *testing.T }

func (l localTripwire) Run(_ context.Context, _ string, req ExecRequest) ExecOutcome {
	l.t.Errorf("AppTown's own machine was asked to run %v", req.Argv)
	return ExecOutcome{RunErr: errors.New("nothing runs on AppTown's own machine")}
}

// onlyOnTheWorker gives a's agent executor the tripwire as its local
// process, keeping the worker the composition gave it.
func onlyOnTheWorker(t *testing.T, a *App) {
	t.Helper()
	a.Work.Stop()
	a.agents = agentExecutionFor(a, localTripwire{t})
	rebuildWork(t, a, func(*workflow.Dependencies) {})
	a.Work.Stop()
}

// agentWorkerChange admits a change of a new project whose run has steps,
// with files committed in the run's folder (they reach the branch as the
// first task's source). It returns the run and its steps by name.
func agentWorkerChange(t *testing.T, a *App, steps []schema.Step, files map[string]string) (*workflow.Run, map[string]*workflow.Step) {
	t.Helper()
	p, err := a.lifecycle.Create(context.Background(), projectlifecycle.Draft{Slug: "agents", Name: "Agents", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.lifecycle.InitializeRepository(t.Context(), p, "", ""); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "agent", Version: 1}, Spec: schema.Spec{
		Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: steps}}}})
	a.Work.Stop()
	ctx := asPerson("alice")
	change, err := a.Work.Create(ctx, workflow.ChangeDraft{ProjectID: p.ID, Title: "agent work"})
	if err == nil {
		change, err = a.Work.Begin(ctx, change.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareChangeWorkspaceForTest(a, p, change); err != nil {
		t.Fatal(err)
	}
	run, err := a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: change.ID, Workflow: "agent"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	if len(files) > 0 {
		ws := workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir)
		for name, body := range files {
			at := filepath.Join(ws, name)
			if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
				t.Fatal(err)
			}
			if target, ok := strings.CutPrefix(body, "link:"); ok {
				if err := os.Symlink(target, at); err != nil {
					t.Fatal(err)
				}
				continue
			}
			// Data and the sourced fake-pi.sh: nothing here runs them as programs.
			if err := os.WriteFile(at, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gittest.Run(t, ws, "add", "-A")
		gittest.Run(t, ws, "-c", "user.name=alice", "-c", "user.email=alice@example.org", "commit", "-q", "-m", "the fixture's files")
	}
	snapshot, err := a.Work.Snapshot(ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*workflow.Step{}
	for _, s := range snapshot.Steps {
		byName[s.Name] = s
	}
	return run, byName
}

// A step the worker cannot run yet is refused before any effect: the
// worker is asked nothing, no invocation is recorded, nothing runs on the
// forge's own machine, and nothing is published. Every refusal comes from
// one check before the step's source is published, and workflowexec's
// TestAnAgentStepTheWorkerCannotRunIsRefusedBeforeAnyEffect covers each
// reason there. Two are kept here: the one read from the folder's own
// unpublished commit, the last to be known, and the one this AppTown's own
// configuration decides.
func TestAnAgentStepTheWorkerCannotRunIsRefusedBeforeAnyEffect(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		with  map[string]any
		files map[string]string
		want  string
	}{
		"an unknown need": {with: map[string]any{"needs": true}, want: "the app needs no-such-kind, which is not in the dependency catalog",
			files: map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  no-such-kind: {}\n"}},
		// AppTown was started without -private-docker.
		"private Docker without the operator's opt-in": {with: map[string]any{"docker": true}, want: "private Docker on a worker is the operator's opt-in: start AppTown with -private-docker"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var asked atomic.Int64
			ca := workertest.NewCA(t)
			srv := workertest.StartWith(t, ca, t.TempDir(), nil, func(h http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					asked.Add(1)
					h.ServeHTTP(w, r)
				})
			})
			a := testApp(t, Config{AgentExecutor: ExecutorDocker, AgentImage: "apptown-agent", ConsoleCLI: fixtureCLI(t), Worker: workerClientConfig(t, ca, srv.URL)})
			with := map[string]any{"prompt": "do it", "executor": "docker"}
			for k, v := range c.with {
				with[k] = v
			}
			run, steps := agentWorkerChange(t, a, []schema.Step{{Name: "build", Uses: "agent/pi", With: with}}, c.files)
			onlyOnTheWorker(t, a)
			source := workflowSourceForTest(t, a, run)
			hub := source.Repository(a.cfg.dataDir)
			before := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch)
			folderBefore, err := gitexec.FolderTip(context.Background(), source.Workspace(a.cfg.dataDir), source.Branch)
			if err != nil {
				t.Fatal(err)
			}
			asked.Store(0)
			if err := a.Work.ExecuteStep(a.asEngine(context.Background()), steps["build"].ID); err != nil {
				t.Fatal(err)
			}
			status, ev := stepStatus(t, a, steps["build"].ID)
			if reason, _ := ev["reason"].(string); status != workflow.StatusError || !strings.Contains(reason, c.want) {
				t.Fatalf("the step is %s: %v", status, ev)
			}
			if n := asked.Load(); n != 0 {
				t.Fatalf("the worker was asked %d times", n)
			}
			if invocations, err := tasks.New(a.DB).Invocations(context.Background(), steps["build"].ID, 1); err != nil || len(invocations) != 0 {
				t.Fatalf("invocations recorded: %+v %v", invocations, err)
			}
			// A refusal, even one read from the folder's own unpublished
			// commits, comes before the run's source is published: the
			// branch and the folder are as they were, and nothing proposed
			// is left in the hub.
			if after := scm.Ref(t.Context(), hub, "refs/heads/"+source.Branch); after != before {
				t.Fatalf("the branch moved from %q to %q", before, after)
			}
			if after, err := gitexec.FolderTip(context.Background(), source.Workspace(a.cfg.dataDir), source.Branch); err != nil || after != folderBefore {
				t.Fatalf("the folder moved from %q to %q (%v)", folderBefore, after, err)
			}
			if left := gittest.Run(t, hub, "for-each-ref", "refs/apptown/incoming"); left != "" {
				t.Fatalf("a proposed source was left in the hub: %s", left)
			}
		})
	}
}

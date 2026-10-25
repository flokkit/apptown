package apptown

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dataprotection"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestDockerRebuildReplayQueuesObservationWithoutBoot(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	_, step := deployRun(t, a)
	a.Work.ExecuteStep(a.asEngine(context.Background()), step)
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil || env.ObservedGeneration != 1 {
		t.Fatalf("initial deploy %+v %v", env, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.reset()
	// The rebuild stops on its cancelled context, retryably: preparation
	// reads (the eval-copy question) run on that context too.
	if _, err := deploymentJobsForTest(a).Rebuild(ctx, "p/main", map[string]any{"deployment": env.ID, "generation": 1}); !errors.Is(err, context.Canceled) || jobs.IsPermanent(err) {
		t.Fatalf("source cancellation became a permanent rebuild failure: %v", err)
	}
	if len(f.joined()) != 0 {
		t.Fatal("cancelled source preparation contacted Docker")
	}
	// sql-guarantee: no owner read lists a deployment's settled attempts; only the count proves no further attempt was admitted.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM deployment_attempts WHERE deployment_id=?`, env.ID)
	store := a.Deployment.Outcomes
	inputs, err := store.InputsAtGeneration(context.Background(), env.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	inputs.ReleaseID, inputs.Rebuild = ids.Nil, true
	base := int64(1)
	at, _, err := store.Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: fmt.Sprintf("rebuild-%d-from-1", env.ID), Inputs: inputs, BaseGeneration: &base})
	if err != nil {
		t.Fatal(err)
	}
	f.reset()
	for i := 0; i < 2; i++ {
		receipt, err := deploymentJobsForTest(a).Rebuild(context.Background(), "p/main", map[string]any{"deployment": env.ID, "generation": 1})
		if err != nil || receipt["skipped"] == nil {
			t.Fatalf("replay %+v %v", receipt, err)
		}
	}
	if calls := f.joined(); len(calls) != 0 {
		t.Fatalf("replayed rebuild contacted Docker: %v", calls)
	}
	if got := attemptStatus(t, a, at.ID); got != deployment.AttemptUncertain {
		t.Fatalf("replayed rebuild's attempt: %s", got)
	}
	if n := jobsInState(t, a, deployment.RecoveryJob, fmt.Sprint(at.ID), "available"); n != 1 {
		t.Fatalf("%d available recovery jobs for the attempt", n)
	}
}

func TestDockerInputsRequireRecordedSource(t *testing.T) {
	t.Parallel()
	a, env := deploymentOutcomeFixture(t)
	store := a.Deployment.Outcomes
	at, _, err := store.Admit(context.Background(), deploymentStartFor(env, "old", "old"))
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := store.InputsFor(context.Background(), at)
	if err != nil || inputs.DockerSource != "" || inputs.Rebuild {
		t.Fatalf("upgrade invented execution inputs: %+v %v", inputs, err)
	}
	if _, _, err := docker.RestoreSource(context.Background(), t.TempDir(), "p", inputs.DockerSource); err == nil {
		t.Fatal("old deployment guessed source")
	}

}

func TestDockerSelectedDeploymentKeepsResolvedSecretsOutOfInputsAndProof(t *testing.T) {
	t.Parallel()
	a, f, ws := secretsWorld(t)
	a.SeedTargets(t.Context())
	for _, name := range []string{"STRIPE_API_KEY", "MIGRATION_DB_ROLE"} {
		if _, err := a.Secrets.Vault.Put(t.Context(), secret.Scope("p", "main", ""), name, "private-value-"+name, "test"); err != nil {
			t.Fatal(err)
		}
		if err := a.Secrets.Bindings.Put(t.Context(), idstest.N(1), secret.Binding{Deployment: "main", Name: name, Store: secret.StoreInternal, Ref: secret.Scope("p", "main", ""), Version: "1", UpdatedBy: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init"}, {"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "selected secret references"}} {
		if _, err := gitexec.Run(t.Context(), ws, args...); err != nil {
			t.Fatal(err)
		}
	}
	art, cleanup, err := docker.PrepareSource(context.Background(), ws, "HEAD", "p", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	targetForLanding, err := a.Deployment.Targets.ByName(t.Context(), deployment.BuiltInTargetName)
	if err != nil {
		t.Fatal(err)
	}
	env, err := a.Deployment.Deployments.EnsureStanding(t.Context(), idstest.N(1), "main", targetForLanding.ID, deployment.Attributes{Feed: "landing", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	store := a.Deployment.Outcomes
	at, _, err := store.Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: "secrets", Inputs: deployment.Inputs{Revision: art.Source.Revision, ArtifactHash: art.Hash, DockerSource: string(art.Body)}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.Deployment.Targets.ByID(t.Context(), env.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.targetProviders.Driver(context.Background(), target, env)
	if err != nil {
		t.Fatal(err)
	}
	o, err := d.Reconcile(context.Background(), env.Placement(), at.Generation, art, "secrets", func(string, ...any) {}, store.Checkpoint(context.Background(), at))
	if err != nil {
		t.Fatal(err)
	}
	var input, proof string
	// sql-guarantee: no secret may be stored anywhere in the attempt's inputs or proof; the stored bytes are the guarantee, and the stored Docker source has no owner read.
	if err := a.DB.QueryRow(`SELECT i.docker_source,a.observed_json FROM deployment_attempt_inputs i JOIN deployment_attempts a ON a.id=i.attempt_id WHERE a.id=?`, at.ID).Scan(&input, &proof); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input+proof, "private-value-") {
		t.Fatal("resolved secret leaked into deployment input or boot proof")
	}
	bindings, err := a.Secrets.Bindings.For(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	generated, _, err := a.Secrets.Vault.Get(t.Context(), bindings["SECRET_KEY_BASE"].Ref, "SECRET_KEY_BASE")
	if err != nil || generated == "" {
		t.Fatal("expected the selected deployment's generated secret")
	}
	if strings.Contains(input+proof, generated) {
		t.Fatal("generated secret leaked into deployment inputs or proof")
	}
	if !strings.Contains(strings.Join(f.joined(), "\n"), "STRIPE_API_KEY=private-value-STRIPE_API_KEY") {
		t.Fatal("secret did not reach selected deployment")
	}
	if _, err := store.Complete(context.Background(), deployment.Completion{Attempt: at, Observed: o}); err != nil {
		t.Fatal(err)
	}
	if proof := mustJSON(attemptNotes(t, a, at.ID)); strings.Contains(proof, "private-value-") {
		t.Fatal("resolved secret leaked into completion receipt")
	}
}

func TestDockerRebuildInspectionFailureDoesNotAdmitReplacement(t *testing.T) {
	t.Parallel()
	a, f := bootWorld(t)
	runID, stepID := deployRun(t, a)
	run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
	if err != nil {
		t.Fatal(err)
	}
	ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
	if _, err := gitexec.Run(t.Context(), ws, "clone", "--no-hardlinks", ws, a.projectRepoPath("p")); err != nil {
		t.Fatal(err)
	}
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil || env.ObservedGeneration != 1 {
		t.Fatalf("initial deployment %+v %v", env, err)
	}
	f.reset()
	f.reply = func(args []string) (string, error, bool) {
		if len(args) > 2 && args[0] == "inspect" && args[2] == docker.RuntimeFormat {
			return "Cannot connect to the Docker daemon", errFakeExit, true
		}
		return "", nil, false
	}
	if _, err := deploymentJobsForTest(a).Rebuild(context.Background(), "p/main", map[string]any{"deployment": env.ID, "generation": 1}); err == nil {
		t.Fatal("failed inspection must stop the rebuild")
	}
	// sql-guarantee: no owner read lists a deployment's settled attempts; only the count proves no further attempt was admitted.
	completionCount(t, a.DB, 1, `SELECT COUNT(*) FROM deployment_attempts WHERE deployment_id=?`, env.ID)
	for _, call := range f.joined() {
		if strings.HasPrefix(call, "run ") || strings.HasPrefix(call, "rm ") || strings.HasPrefix(call, "build ") {
			t.Fatalf("inspection failure caused replacement: %s", call)
		}
	}
}

func TestLandingContractDecisionUsesSelectedCommit(t *testing.T) {
	t.Parallel()
	for _, drift := range []string{"deleted", "malformed", "added"} {
		t.Run(drift, func(t *testing.T) {
			a, _ := bootWorld(t)
			runID, stepID := deployRun(t, a)
			run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
			if err != nil {
				t.Fatal(err)
			}
			ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
			path := filepath.Join(ws, ".apptown/app.yaml")
			if drift == "added" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				for _, args := range [][]string{{"add", "."}, {"-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "no runtime contract"}} {
					if _, err := gitexec.Run(t.Context(), ws, args...); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(path, []byte(bootFixtureYAML), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if drift == "deleted" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte("not: [valid"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
			if step := readStep(t, a, stepID); step.Status != workflow.StatusPass {
				t.Fatalf("deploy step: %s", step.Status)
			}
			count := 1
			if drift == "added" {
				count = 0
			}
			if n := deploymentsMatching(t, a, func(d deployment.ProjectDeployment) bool { return d.ObservedGeneration == 1 }); n != count {
				t.Fatalf("%d deployments at generation 1, want %d", n, count)
			}
		})
	}
}

// A built-in deployment restored with AppTown is rebuilt by the first
// start, and its data goes back in that rebuild before anything of its app
// starts. A whole set fills it and the app comes up; a partial set, or
// none, starts nothing of the app and says why; either way the hold is
// lifted, so the deployment is never left waiting on a rebuild that has
// ended.
//
// Kills: a partial set reported filled with the app started on part of its
// data; a fill skipped by the rebuild; a hold left after the rebuild ended.
func TestARestoredDeploymentIsRebuiltWithItsDataBeforeItsAppStarts(t *testing.T) {
	for _, scenario := range []string{"whole", "partial", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			a, f := bootWorld(t)
			runID, stepID := deployRun(t, a)
			run, err := workflow.ReadRunIn(context.Background(), a.DB, runID)
			if err != nil {
				t.Fatal(err)
			}
			ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
			// The app keeps its files in its data volume and needs nothing
			// else, so the fake daemon has no database to answer for.
			if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(strings.Split(bootFixtureYAML, "needs:")[0]), 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := gitexec.Run(t.Context(), ws, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qam", "no needs"); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if _, err := gitexec.Run(t.Context(), ws, "clone", "--no-hardlinks", ws, a.projectRepoPath("p")); err != nil {
				t.Fatal(err)
			}
			a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
			ctx := t.Context()
			dep, err := a.Deployment.Deployments.ByName(ctx, idstest.N(1), "main")
			if err != nil || dep.ObservedGeneration != 1 {
				t.Fatalf("initial deployment %+v %v", dep, err)
			}
			// The set the archive held: whole (nothing failed), partial (its
			// database's copy failed), or none at all.
			var set ids.ID
			if scenario != "missing" {
				b := &dataprotection.Backup{ProjectID: dep.ProjectID, Deployment: dep.Name, TakenAt: "2026-10-23T00:00:00Z", Trigger: dataprotection.TriggerAppTownBackup,
					Path: t.TempDir(), Status: dataprotection.BackupOK, Store: dataprotection.StoreAppTownDisk}
				if scenario == "partial" {
					b.Status = dataprotection.BackupPartial
					b.Items = []dataprotection.BackupItem{{Kind: "need", Name: "postgres", File: "postgres.dump", Error: "pg_dump: connection refused"}}
				}
				if set, err = dataprotection.NewBackups(a.DB).Record(ctx, b, "test"); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.DataProtection.RecordFill(ctx, dep.ID, set, "the backup could not stop its app", true); err != nil {
				t.Fatal(err)
			}
			if err := a.deployments.HoldForFill(ctx, []ids.ID{dep.ID}); err != nil {
				t.Fatal(err)
			}
			// A fresh host: the app's container is not there.
			names := mainNames(t, a)
			if _, err := f.Run(ctx, "", docker.Command{Args: []string{"rm", "-f", names.App()}}); err != nil {
				t.Fatal(err)
			}
			f.reset()
			if _, err := deploymentJobsForTest(a).Rebuild(ctx, "p/main", map[string]any{"deployment": dep.ID, "generation": 1}); err != nil && scenario == "whole" {
				t.Fatalf("the rebuild with a whole set: %v", err)
			}
			started := false
			for _, call := range f.matching("run ") {
				if containerNameOf(call) == names.App() {
					started = true
				}
			}
			fills, err := a.DataProtection.FillsOf(ctx, []ids.ID{dep.ID})
			if err != nil {
				t.Fatal(err)
			}
			fill := fills[dep.ID]
			after, err := a.Deployment.Deployments.ByID(ctx, dep.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.RestoreCheck != "" {
				t.Fatalf("the hold outlived the rebuild: %q", after.RestoreCheck)
			}
			switch scenario {
			case "whole":
				if !started || fill.State != dataprotection.FillFilled {
					t.Fatalf("a whole set: app started %v, fill %+v", started, fill)
				}
			default:
				if started {
					t.Fatalf("the app started without its data:\n%s", strings.Join(f.joined(), "\n"))
				}
				want := map[string]string{"partial": "connection refused", "missing": "could not stop its app"}[scenario]
				if fill.State != dataprotection.FillFailed || !strings.Contains(fill.Detail, want) {
					t.Fatalf("fill %+v, want failed naming %q", fill, want)
				}
			}
		})
	}
}

func TestRestoredDatabaseBindsNewStorageBeforeEngineAndImport(t *testing.T) {
	for _, scenario := range []string{"fresh", "source-volume-remains", "bad-file"} {
		t.Run(scenario, func(t *testing.T) {
			a, f := bootWorld(t)
			runID, stepID := deployRun(t, a)
			run, err := workflow.ReadRunIn(t.Context(), a.DB, runID)
			if err != nil {
				t.Fatal(err)
			}
			ws := requireLocation(t)(workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir))
			if _, err := gitexec.Run(t.Context(), ws, "clone", "--no-hardlinks", ws, a.projectRepoPath("p")); err != nil {
				t.Fatal(err)
			}
			a.Work.ExecuteStep(a.asEngine(t.Context()), stepID)
			dep, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
			if err != nil || dep.ObservedGeneration != 1 {
				step, _ := workflow.ReadStepIn(t.Context(), a.DB, stepID)
				t.Fatalf("initial deployment: %+v %v step=%+v calls=%s", dep, err, step, strings.Join(f.joined(), "\n"))
			}
			names := mainNames(t, a)
			volume := docker.Current.NeedVolume(names.Stack(), "postgres")
			container := docker.Current.Service(names.Stack(), "postgres")
			owner := a.Deployment.Deployments.StorageOf(dep)
			source, found, err := owner.Get(t.Context(), "postgres", volume)
			if err != nil || !found {
				t.Fatalf("source binding: %+v %t %v", source, found, err)
			}
			dir := t.TempDir()
			data := []byte("logical database fixture")
			if err := os.WriteFile(filepath.Join(dir, "postgres.dump"), data, 0600); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			item := dataprotection.BackupItem{Kind: "need", Name: "postgres", Volume: volume, File: "postgres.dump", SHA256: hex.EncodeToString(digest[:]), Copy: dataprotection.CopyDeclared, Restore: "restore fixture"}
			if scenario == "bad-file" {
				item.SHA256 = strings.Repeat("0", 64)
			}
			b := &dataprotection.Backup{ProjectID: dep.ProjectID, Deployment: dep.Name, TakenAt: "2026-10-25T00:00:00Z", Trigger: dataprotection.TriggerAppTownBackup, Path: dir, Status: dataprotection.BackupOK, Store: dataprotection.StoreAppTownDisk, Items: []dataprotection.BackupItem{item}}
			set, err := dataprotection.NewBackups(a.DB).Record(t.Context(), b, "test")
			if err != nil {
				t.Fatal(err)
			}
			if err := a.DataProtection.RecordFill(t.Context(), dep.ID, set, "", true); err != nil {
				t.Fatal(err)
			}
			if err := a.Deployment.Deployments.HoldForFill(t.Context(), []ids.ID{dep.ID}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{names.App(), container} {
				if _, err := f.run("rm", "-f", name); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "source-volume-remains" {
				if _, err := f.run("volume", "rm", volume); err != nil {
					t.Fatal(err)
				}
			}
			f.reply = func(args []string) (string, error, bool) {
				line := strings.Join(args, " ")
				if strings.Contains(line, ".Mounts") {
					if strings.Contains(line, ".Source") {
						return "volume\t" + volume + "\t/fixture\t/var/lib/postgresql/data\n", nil, true
					}
					return volume + "\t/var/lib/postgresql/data\n", nil, true
				}
				if strings.Contains(line, "{{json .Config}}") {
					return `{ "Image":"postgres:16-alpine", "Env":["POSTGRES_USER=app","POSTGRES_PASSWORD=test","POSTGRES_DB=app"] }`, nil, true
				}
				if strings.Contains(line, ".Config.Env") {
					return "POSTGRES_USER=app\nPOSTGRES_PASSWORD=test\nPOSTGRES_DB=app\n", nil, true
				}
				return "", nil, false
			}
			starts, imports := 0, 0
			f.command = func(ctx context.Context, _ string, req docker.Command) ([]byte, error) {
				if len(req.Args) > 0 && req.Args[0] == "run" && containerNameOf(strings.Join(req.Args, " ")) == container {
					starts++
					binding, ok, err := owner.Get(ctx, "postgres", volume)
					if err != nil || !ok || binding.Origin != "restored" || binding.VolumeCreated.Equal(source.VolumeCreated) {
						t.Errorf("engine started before target binding: %+v %t %v", binding, ok, err)
					}
				}
				if req.Stdin != nil {
					imports++
					_, err := io.Copy(io.Discard, req.Stdin)
					return nil, err
				}
				return f.run(req.Args...)
			}
			f.reset()
			_, err = deploymentJobsForTest(a).Rebuild(t.Context(), "p/main", map[string]any{"deployment": dep.ID, "generation": 1})
			if scenario == "fresh" {
				if err != nil || starts != 1 || imports != 1 {
					t.Fatalf("fresh restore: starts=%d imports=%d err=%v", starts, imports, err)
				}
			} else if starts != 0 || imports != 0 {
				t.Fatalf("refused restore had effects: starts=%d imports=%d err=%v", starts, imports, err)
			}
		})
	}
}

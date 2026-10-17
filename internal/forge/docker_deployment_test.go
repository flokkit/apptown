package forge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
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
	binding := a.Secrets.BindingsFor(t.Context(), idstest.N(1), "main")["SECRET_KEY_BASE"]
	generated, _, found := a.Secrets.Stored(t.Context(), binding.Ref, "SECRET_KEY_BASE")
	if !found || generated == "" {
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
	ws := workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir)
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
			ws := workflowSourceForTest(t, a, run).Workspace(a.cfg.dataDir)
			path := filepath.Join(ws, ".forge/app.yaml")
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

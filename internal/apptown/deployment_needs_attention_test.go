package apptown

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/dbtest"
	dockerhost "github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// receiptsOf reads a project's ledger lines of one kind, newest first.
func receiptsOf(t *testing.T, a *App, projectID ids.ID, kind string) []audit.Entry {
	t.Helper()
	got, err := audit.Recent(context.Background(), a.DB, audit.Filter{ProjectID: projectID, KindPrefix: kind})
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Entry
	for _, e := range got {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func readinessOf(t *testing.T, a *App, env *deployment.Deployment) deployment.Readiness {
	t.Helper()
	now, err := a.Deployment.Deployments.ByName(t.Context(), env.ProjectID, env.Name)
	if err != nil {
		t.Fatal(err)
	}
	return now.Readiness
}

// A deployment whose attempt is still open while the deployment reads
// reconciled does not stop startup: it is marked as needing attention
// (readiness uncertain, its attempt uncertain and queued for observation,
// one ledger line however often AppTown starts), and startup goes on.
func TestStartupFlagsADeploymentWhoseRecordsContradictEachOther(t *testing.T) {
	t.Parallel()
	a, env, _, at := deploymentReleaseFixture(t)
	// sql-guarantee: an open attempt beside a reconciled deployment is the contradiction under test; settlement moves both together, so no owner operation produces it.
	completionExec(t, a.DB, `UPDATE deployments SET readiness='reconciled' WHERE id=$1`, env.ID)
	for i := 0; i < 2; i++ {
		if err := a.RecoverDeployments(context.Background()); err != nil {
			t.Fatalf("startup stopped over one contradictory deployment: %v", err)
		}
	}
	if r := readinessOf(t, a, env); r != deployment.ReadinessUncertain {
		t.Fatalf("readiness %s, want uncertain", r)
	}
	if _, status, _, err := a.Deployment.Outcomes.RecoveryAttempt(context.Background(), at.ID); err != nil || status != deployment.AttemptUncertain {
		t.Fatalf("attempt: %s %v", status, err)
	}
	if got := receiptsOf(t, a, env.ProjectID, "deployment.needs-attention"); len(got) != 1 || got[0].Subject != env.Name {
		t.Fatalf("needs-attention receipts: %+v", got)
	}
}

// A release still releasing after its attempt ended does not stop
// startup; Check again settles it as its attempt's settlement would have
// (an attempt a newer generation superseded fails its release), and the
// deployment reads reconciled again.
func TestARefusedSettlementIsFlaggedAndCheckAgainSettlesIt(t *testing.T) {
	t.Parallel()
	a, env := deploymentOutcomeFixture(t)
	testAdministrator(t, a, "alice")
	started, err := deployment.NewReleases(a.DB).Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: env.ProjectID, DeploymentID: env.ID, Deployment: env.Name, Revision: "selected-revision", Actor: "alice"}, "prepared for settlement")
	if err != nil {
		t.Fatal(err)
	}
	s := a.Deployment.Outcomes
	old, _, err := s.Admit(context.Background(), deployment.Start{DeploymentID: env.ID, TargetID: env.TargetID, Key: "release-old",
		Inputs: deployment.Inputs{ReleaseID: started.ID, Revision: "selected-revision", ImageDigest: "selected-image", ArtifactHash: "selected-artifact"}})
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: Complete only the attempt while leaving its release releasing; Outcomes.Complete settles both atomically, so it cannot construct this contradiction.
	completionExec(t, a.DB, `UPDATE deployment_attempts SET status='done', terminal=true, observed_json=$1, finished_at=now() WHERE id=$2`, mustJSON(deploymentCompletionFor(old, "old").Observed), old.ID)
	next, _, err := s.Admit(context.Background(), deploymentStartFor(env, "next", "next"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(context.Background(), deploymentCompletionFor(next, "next")); err != nil {
		t.Fatal(err)
	}
	if err := a.RecoverDeployments(context.Background()); err != nil {
		t.Fatalf("startup stopped over one contradictory release: %v", err)
	}
	if r := readinessOf(t, a, env); r != deployment.ReadinessUncertain {
		t.Fatalf("readiness %s, want uncertain", r)
	}
	if got := receiptsOf(t, a, env.ProjectID, "deployment.needs-attention"); len(got) != 1 {
		t.Fatalf("needs-attention receipts: %+v", got)
	}
	result, err := a.Deployment.CheckAgain(asPerson("alice"), env.ProjectID, env.Name)
	if err != nil || len(result.Checked) != 1 || result.Checked[0].Outcome != "failed" {
		t.Fatalf("check again: %+v %v", result, err)
	}
	rel, err := a.Deployment.Releases.ByNumber(context.Background(), env.ProjectID, started.Number)
	if err != nil || rel.State != deployment.ReleaseFailed {
		t.Fatalf("release after the check: %+v %v", rel, err)
	}
	if r := readinessOf(t, a, env); r != deployment.Reconciled {
		t.Fatalf("readiness after the check %s, want reconciled", r)
	}
	if got := receiptsOf(t, a, env.ProjectID, "deployment.checked-again"); len(got) != 1 || got[0].Actor != "alice" {
		t.Fatalf("check receipts: %+v", got)
	}
	if _, err := a.Deployment.CheckAgain(asPerson("alice"), env.ProjectID, env.Name); err == nil {
		t.Fatal("a deployment with nothing in doubt was checked again")
	}
}

// Check again observes an uncertain attempt on its target and settles what
// it sees: the container its boot recorded running with that image settles
// it applied; the container gone settles it failed; a host that cannot be
// asked leaves it uncertain, still needing attention. Each check is in the
// ledger with who pressed it and what was observed.
func TestCheckAgainSettlesWhatItObserves(t *testing.T) {
	t.Parallel()
	for _, observed := range []string{"running", "gone", "unreachable"} {
		t.Run(observed, func(t *testing.T) {
			t.Parallel()
			a, fake := bootWorld(t)
			testAdministrator(t, a, "alice")
			a.SeedTargets(t.Context())
			_, stepID := deployRun(t, a)
			// The boot is recorded, its settlement is not: the attempt is
			// uncertain, with its boot proof.
			dbtest.FailNextStatement(t, a.DB, "-- name: FinishAttempt")
			a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
			env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
			if err != nil || env.Readiness != deployment.ReadinessUncertain {
				t.Fatalf("fixture: %+v %v", env, err)
			}
			fake.reply = func(args []string) (string, error, bool) {
				if len(args) > 2 && args[0] == "inspect" && args[2] == dockerhost.RuntimeFormat {
					name := args[len(args)-1]
					switch observed {
					case "gone":
						return "Error response from daemon: No such object: " + name, errFakeExit, true
					case "unreachable":
						return "Cannot connect to the Docker daemon", errFakeExit, true
					}
				}
				return "", nil, false
			}
			result, err := a.Deployment.CheckAgain(asPerson("alice"), env.ProjectID, env.Name)
			if err != nil || len(result.Checked) != 1 {
				t.Fatalf("check again: %+v %v", result, err)
			}
			attempt := result.Checked[0].Attempt
			_, status, _, err := a.Deployment.Outcomes.RecoveryAttempt(context.Background(), attempt)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]deployment.AttemptState{"running": deployment.AttemptDone, "gone": deployment.AttemptFailed, "unreachable": deployment.AttemptUncertain}[observed]
			wantReadiness := map[string]deployment.Readiness{"running": deployment.Reconciled, "gone": deployment.ReadinessFailed, "unreachable": deployment.ReadinessUncertain}[observed]
			if status != want || readinessOf(t, a, env) != wantReadiness {
				t.Fatalf("observed %s: attempt %s readiness %s; result %+v", observed, status, readinessOf(t, a, env), result)
			}
			receipts := receiptsOf(t, a, env.ProjectID, "deployment.checked-again")
			if len(receipts) != 1 || receipts[0].Actor != "alice" {
				t.Fatalf("check receipts: %+v", receipts)
			}
			var payload struct {
				Checked []deployment.RecoveryResult `json:"checked"`
			}
			if err := json.Unmarshal(receipts[0].Payload, &payload); err != nil || len(payload.Checked) != 1 || payload.Checked[0].Outcome == "" {
				t.Fatalf("the receipt does not say what was observed: %s %v", receipts[0].Payload, err)
			}
			if observed == "gone" && !strings.Contains(payload.Checked[0].Reason, "gone") {
				t.Fatalf("the receipt does not say the container was gone: %+v", payload.Checked[0])
			}
		})
	}
}

// A release whose boot fails after it started changing the host (here its
// migration exits non-zero) ends failed, not uncertain: nothing is left in
// doubt, so the next release is admitted and Check again has nothing to do.
//
// Kills: a needs/parts/app phase failure left uncertain on the landing path.
func TestAReleaseWhoseMigrationFailsEndsFailed(t *testing.T) {
	t.Parallel()
	a, fake := bootWorld(t)
	testAdministrator(t, a, "alice")
	_, stepID := deployRun(t, a)
	fake.reply = func(args []string) (string, error, bool) {
		if len(args) > 0 && args[0] == "run" && strings.Contains(strings.Join(args, " "), "--rm --init --name ") && strings.Contains(strings.Join(args, " "), "migrate") {
			return "migration failed", errFakeExit, true
		}
		return "", nil, false
	}
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil {
		t.Fatal(err)
	}
	if env.Readiness == deployment.ReadinessUncertain || len(openAttempts(t, a)) != 0 {
		t.Fatalf("a failed migration left the deployment in doubt: readiness %s, open %v", env.Readiness, openAttempts(t, a))
	}
	if _, err := a.Deployment.CheckAgain(asPerson("alice"), env.ProjectID, env.Name); err == nil {
		t.Fatal("Check again found something in doubt after a failed release")
	}
}

// An attempt left uncertain with no record that its new version came up
// (the host stopped answering right after the boot, as when AppTown stops
// mid-boot) is settled failed by Check again once the host answers, and
// the deployment takes a new release.
//
// Kills: Resume leaving an attempt without a boot record uncertain forever.
func TestCheckAgainFailsAnAttemptWithNoRecordOfItsBoot(t *testing.T) {
	t.Parallel()
	a, fake := bootWorld(t)
	testAdministrator(t, a, "alice")
	_, stepID := deployRun(t, a)
	unreachable := true
	fake.reply = func(args []string) (string, error, bool) {
		if unreachable && len(args) > 2 && args[0] == "inspect" && args[2] == dockerhost.RuntimeFormat {
			return "Cannot connect to the Docker daemon", errFakeExit, true
		}
		return "", nil, false
	}
	a.Work.ExecuteStep(a.asEngine(context.Background()), stepID)
	env, err := a.Deployment.Deployments.ByName(t.Context(), idstest.N(1), "main")
	if err != nil || env.Readiness != deployment.ReadinessUncertain {
		t.Fatalf("fixture: the attempt is not in doubt: %+v %v", env, err)
	}
	unreachable = false
	result, err := a.Deployment.CheckAgain(asPerson("alice"), env.ProjectID, env.Name)
	if err != nil || len(result.Checked) != 1 {
		t.Fatalf("check again: %+v %v", result, err)
	}
	if status := attemptStatus(t, a, result.Checked[0].Attempt); status != deployment.AttemptFailed {
		t.Fatalf("an attempt with no boot record: %s (%+v)", status, result.Checked[0])
	}
	if r := readinessOf(t, a, env); r == deployment.ReadinessUncertain {
		t.Fatalf("readiness still %s", r)
	}
	if _, _, err := a.Deployment.Outcomes.Admit(context.Background(), deploymentStartFor(env, "after", "after")); err != nil {
		t.Fatalf("the next release is refused: %v", err)
	}
}

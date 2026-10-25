package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type outcomeFixture struct {
	db       *database.DB
	outcomes *Outcomes
}

func deploymentOutcomeFixture(t *testing.T) (*outcomeFixture, *Deployment) {
	t.Helper()
	db := testDB(t)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "deployment-outcomes", Name: "Deployment outcomes"})
	})
	if err != nil {
		t.Fatal(err)
	}
	targets := NewTargets(db, nil)
	target, err := targets.Put(context.Background(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	deployments := NewDeployments(db)
	dep, err := deployments.EnsureStanding(t.Context(), pid, "main", target.ID, (Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	return &outcomeFixture{db: db, outcomes: NewOutcomes(db, nil)}, dep
}

func deploymentStartFor(dep *Deployment, key, label string) Start {
	return Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: key,
		Inputs: Inputs{Revision: label + "-revision", ImageDigest: label + "-image", ArtifactHash: label + "-artifact"}}
}

func deploymentCompletionFor(at Attempt, label string) Completion {
	return Completion{Attempt: at,
		Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy", URL: "https://" + label + ".example",
			Handle: map[string]any{"container": label, "hostPort": 40001}}}
}

func TestDeploymentOutcomeReplayPreservesOriginalEvidence(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint("failure=", failure), func(t *testing.T) {
			f, dep := deploymentOutcomeFixture(t)
			s := f.outcomes
			at, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "replay", "original"))
			if err != nil {
				t.Fatal(err)
			}
			finish := func(label string) (Outcome, error) {
				if failure {
					return s.Fail(context.Background(), at, errors.New(label))
				}
				return s.Complete(context.Background(), deploymentCompletionFor(at, label))
			}
			if out, err := finish("original"); err != nil || out != Applied {
				t.Fatalf("first outcome: %s %v", out, err)
			}
			// The generated row read selects every column, so equal rows mean nothing was rewritten.
			before := storedAttempt(t, f.db, at.ID)
			if out, err := finish("different-replay"); err != nil || out != AlreadyApplied {
				t.Fatalf("replayed outcome: %s %v", out, err)
			}
			if after := storedAttempt(t, f.db, at.ID); !reflect.DeepEqual(before, after) {
				t.Fatalf("replay replaced evidence: %+v -> %+v", before, after)
			}
			var conflict error
			if failure {
				_, conflict = s.Complete(context.Background(), deploymentCompletionFor(at, "success-after-failure"))
			} else {
				_, conflict = s.Fail(context.Background(), at, errors.New("failure-after-success"))
				if d := storedDeployment(t, f.db, dep.ID); d.ReleaseSHA != "original-revision" || d.ImageDigest != "original-image" || d.URL != "https://original.example" {
					t.Fatalf("contradiction rewrote the deployment: %q %q %q", d.ReleaseSHA, d.ImageDigest, d.URL)
				}
			}
			if !errors.Is(conflict, ErrOutcomeConflict) {
				t.Fatalf("contradictory terminal result: %v", conflict)
			}
		})
	}
}

func TestDeploymentOutcomeRequiresMatchingIdentity(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	s := f.outcomes
	at, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "identity", "identity"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewDeployments(f.db).EnsureStanding(t.Context(), dep.ProjectID, "other", dep.TargetID, (Attributes{}).WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	otherEnv := other.ID
	for _, invalid := range []Attempt{
		{}, {ID: at.ID, DeploymentID: dep.ID, Generation: 0}, {ID: idstest.N(999999), DeploymentID: dep.ID, Generation: at.Generation},
		{ID: at.ID, DeploymentID: otherEnv, Generation: at.Generation}, {ID: at.ID, DeploymentID: dep.ID, Generation: at.Generation + 1},
		{ID: at.ID, DeploymentID: idstest.N(999999), Generation: at.Generation},
	} {
		if out, err := s.Complete(context.Background(), deploymentCompletionFor(invalid, "wrong")); err == nil || out != "" {
			t.Fatalf("invalid identity %+v: %s %v", invalid, out, err)
		}
		if out, err := s.Fail(context.Background(), invalid, errors.New("wrong")); err == nil || out != "" {
			t.Fatalf("invalid failure identity %+v: %s %v", invalid, out, err)
		}
	}
	wantN(t, "running attempts", attemptsIn(t, f.db, AttemptRunning), 1)
	wantCount(t, "unobserved deployments", deploymentsWhere(t, f.db, func(d *Deployment) bool { return d.ObservedGeneration == 0 && d.URL == "" }), 2)
	if err := f.db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, dep.ProjectID) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(context.Background(), deploymentCompletionFor(at, "deleted")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted deployment: %v", err)
	}
}

func TestDeploymentOutcomeRejectsUnconfirmedObservation(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	s := f.outcomes
	at, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "observe", "observe"))
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []deploy.Observation{
		{Generation: at.Generation + 1, Rollout: "active"},
		{Generation: at.Generation, Rollout: "rolling"},
		{Generation: at.Generation, Rollout: "absent"},
	} {
		if out, err := s.Complete(context.Background(), Completion{Attempt: at, Observed: observation}); err == nil || out != "" {
			t.Fatalf("unconfirmed observation %+v: %s %v", observation, out, err)
		}
	}
	wantN(t, "running attempts", attemptsIn(t, f.db, AttemptRunning), 1)
}

func TestDeploymentOutcomeRollsBackEveryWrite(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		for _, statement := range []string{"UPDATE deployments", "-- name: FinishAttempt"} {
			t.Run(fmt.Sprint(failure, statement), func(t *testing.T) {
				f, dep := deploymentOutcomeFixture(t)
				s := f.outcomes
				at, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "atomic", "atomic"))
				if err != nil {
					t.Fatal(err)
				}
				dbtest.FailNextStatement(t, f.db, statement)
				var out Outcome
				if failure {
					out, err = s.Fail(context.Background(), at, errors.New("provider failure"))
				} else {
					out, err = s.Complete(context.Background(), deploymentCompletionFor(at, "atomic"))
				}
				if !errors.Is(err, database.ErrInjectedFailure) || out != "" {
					t.Fatalf("failed write: %s %v", out, err)
				}
				wantCount(t, "untouched running attempts", attemptsWhere(t, f.db, func(a deploymentsql.DeploymentAttempt) bool {
					return string(a.ObservedJson) == "{}" && !a.FinishedAt.Valid
				}, AttemptRunning), 1)
				wantCount(t, "unobserved deployments", deploymentsWhere(t, f.db, func(d *Deployment) bool {
					return d.DesiredGeneration == 1 && d.ObservedGeneration == 0 && d.Readiness == Reconciling && d.URL == ""
				}), 1)
				if out, err := s.Complete(context.Background(), deploymentCompletionFor(at, "retry")); err != nil || out != Applied {
					t.Fatalf("retry after rollback: %s %v", out, err)
				}
			})
		}
	}
}

func TestDeploymentOutcomeConcurrentCompletionHasOneWinner(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	s := f.outcomes
	at, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "race", "race"))
	if err != nil {
		t.Fatal(err)
	}
	results := race(8, func(i int) int {
		var out Outcome
		var err error
		if i%2 == 0 {
			out, err = s.Complete(context.Background(), deploymentCompletionFor(at, fmt.Sprint("winner-", i)))
		} else {
			out, err = s.Fail(context.Background(), at, errors.New(fmt.Sprint("failure-", i)))
		}
		if out == Applied && err == nil {
			return 1
		}
		if (err == nil && out == AlreadyApplied) || errors.Is(err, ErrOutcomeConflict) {
			return 0
		}
		t.Errorf("unexpected concurrent result: %s %v", out, err)
		return -1
	})
	if count(results, 1) != 1 {
		t.Fatalf("completion winners: %v", results)
	}
	wantCount(t, "settled attempts matching their deployment", attemptsWhere(t, f.db, func(a deploymentsql.DeploymentAttempt) bool {
		e := storedDeployment(t, f.db, a.DeploymentID)
		return (a.Status == AttemptDone.String() && e.ObservedGeneration == a.Generation && e.Readiness == Reconciled) ||
			(a.Status == AttemptFailed.String() && e.ObservedGeneration == 0 && e.Readiness == ReadinessFailed)
	}, AttemptDone, AttemptFailed), 1)
}

func TestDeploymentOutcomeAdmissionRacesCompletion(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	s := f.outcomes
	old, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "old", "old"))
	if err != nil {
		t.Fatal(err)
	}
	results := race(8, func(i int) int {
		if i%2 == 0 {
			at, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "new", "new"))
			if err != nil || at.Generation != old.Generation+1 {
				t.Errorf("new admission: %+v %v", at, err)
			}
		} else {
			if _, err := s.Complete(context.Background(), deploymentCompletionFor(old, "old")); err != nil {
				t.Error(err)
			}
		}
		return 0
	})
	_ = results
	wantCount(t, "new running attempts", attemptsWhere(t, f.db, func(a deploymentsql.DeploymentAttempt) bool {
		return a.IdempotencyKey == "new" && a.Generation == 2
	}, AttemptRunning), 1)
	if d := storedDeployment(t, f.db, dep.ID); d.DesiredGeneration != 2 || d.ObservedGeneration > 1 || d.Readiness != Reconciling {
		t.Fatalf("deployment after the race: desired %d observed %d %s", d.DesiredGeneration, d.ObservedGeneration, d.Readiness)
	}
	if out, err := s.Fail(context.Background(), old, errors.New("late failure")); err != nil || out != Superseded {
		t.Fatalf("old failure after race: %s %v", out, err)
	}
	newAttempt, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "new", "new"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := s.Complete(context.Background(), deploymentCompletionFor(newAttempt, "new")); err != nil || out != Applied {
		t.Fatalf("new completion: %s %v", out, err)
	}
	wantCount(t, "deployments serving the new generation", deploymentsWhere(t, f.db, func(d *Deployment) bool {
		return d.DesiredGeneration == 2 && d.ObservedGeneration == 2 && d.ReleaseSHA == "new-revision"
	}), 1)
}

func TestDeploymentOutcomeLateCompletionPreservesNewGeneration(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			f, dep := deploymentOutcomeFixture(t)
			old, _, err := (f.outcomes).Admit(context.Background(), deploymentStartFor(dep, "old", "old"))
			oldID, oldGen := old.ID, old.Generation
			if err != nil {
				t.Fatal(err)
			}
			next, _, err := (f.outcomes).Admit(context.Background(), deploymentStartFor(dep, "new", "new"))
			newID, newGen := next.ID, next.Generation
			if err != nil {
				t.Fatal(err)
			}
			if failure {
				out, err := (f.outcomes).Fail(context.Background(), Attempt{ID: oldID, DeploymentID: dep.ID, Generation: oldGen}, errors.New("old provider failure"))
				if err != nil || out != Superseded {
					t.Fatalf("late failure: %s %v", out, err)
				}
			} else {
				out, err := (f.outcomes).Complete(context.Background(), Completion{Attempt: Attempt{ID: oldID, DeploymentID: dep.ID, Generation: oldGen}, Observed: deploy.Observation{Generation: oldGen, Rollout: "active", URL: "https://old.example"}})
				if err != nil || out != Superseded {
					t.Fatalf("late success: %s %v", out, err)
				}
			}
			if a := storedAttempt(t, f.db, oldID); a.Status != AttemptSuperseded.String() {
				t.Fatalf("old attempt: %s", a.Status)
			}
			if a := storedAttempt(t, f.db, newID); a.Status != AttemptRunning.String() {
				t.Fatalf("new attempt: %s", a.Status)
			}
			if d := storedDeployment(t, f.db, dep.ID); d.DesiredGeneration != newGen || d.ObservedGeneration != 0 || d.Readiness != Reconciling || d.URL != "" || d.ReleaseSHA != "" {
				t.Fatalf("late outcome touched the new generation: %+v", d)
			}
		})
	}
}

func TestDeploymentReleaseAdmissionPersistsImmutableInputs(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	s := f.outcomes
	start := deploymentStartFor(dep, "immutable", "selected")
	start.Inputs.Address = &AddressSnapshot{DeploymentID: dep.ID, TargetID: dep.TargetID, Revision: 1, Ingress: deploy.Ingress{Hosts: []string{"selected.apps.example"}, URL: "https://selected.apps.example", CertificateDomain: "apps.example"}}
	dbtest.FailNextStatement(t, f.db, "INSERT INTO deployment_attempt_inputs")
	if _, _, err := s.Admit(context.Background(), start); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("input persistence failure: %v", err)
	}
	wantN(t, "attempts", attemptsIn(t, f.db), 0)
	wantCount(t, "unadmitted deployments", deploymentsWhere(t, f.db, func(d *Deployment) bool { return d.DesiredGeneration == 0 }), 1)
	at, _, err := s.Admit(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if inputs, err := s.InputsFor(context.Background(), at); err != nil || !inputs.Same(start.Inputs) {
		t.Fatalf("admitted inputs: %+v %v", inputs, err)
	}
	changed := start
	address := start.Inputs.Address.clone()
	address.Ingress.Hosts[0] = "changed.apps.example"
	address.Ingress.URL = "https://changed.apps.example"
	changed.Inputs.Address = &address
	if _, _, err := s.Admit(context.Background(), changed); err == nil {
		t.Fatal("one key accepted different ingress inputs")
	}
	start.Inputs.Revision = "changed"
	if _, _, err := s.Admit(context.Background(), start); err == nil {
		t.Fatal("one key accepted different deployment inputs")
	}
	wantN(t, "attempts", attemptsIn(t, f.db), 1)
}

func TestDeploymentReleaseAdmissionRequiresInputRow(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	mustExec(t, f.db, `CREATE FUNCTION veto_inputs() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	mustExec(t, f.db, `CREATE TRIGGER veto_inputs BEFORE INSERT ON deployment_attempt_inputs FOR EACH ROW EXECUTE FUNCTION veto_inputs()`)
	start := deploymentStartFor(dep, "immutable", "selected")
	if at, _, err := f.outcomes.Admit(context.Background(), start); err == nil {
		t.Fatalf("admitted work without its inputs: %+v", at)
	}
	wantN(t, "attempts", attemptsIn(t, f.db), 0)
	// sql-guarantee: a vetoed admission must leave no orphan inputs row; no owner read lists inputs without their attempt.
	dbtest.Count(t, f.db, 0, `SELECT COUNT(*) FROM deployment_attempt_inputs`)
	wantCount(t, "unadmitted deployments", deploymentsWhere(t, f.db, func(d *Deployment) bool { return d.DesiredGeneration == 0 }), 1)
	mustExec(t, f.db, `DROP TRIGGER veto_inputs ON deployment_attempt_inputs`)
	at, _, err := f.outcomes.Admit(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if inputs, err := f.outcomes.InputsFor(context.Background(), at); err != nil || !inputs.Same(start.Inputs) {
		t.Fatalf("retry lost its selected inputs: %+v %v", inputs, err)
	}
}

// A deployment's outcome is the record of an effect already made: it is
// settled though the context of whoever finished the effect has ended, and
// so is an uncertain one. Neither is lost to cancellation.
func TestADeploymentsOutcomeIsSettledThoughItsCallerHasGone(t *testing.T) {
	t.Parallel()
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	for _, uncertain := range []bool{false, true} {
		f, dep := deploymentOutcomeFixture(t)
		f.outcomes = NewOutcomes(f.db, noFollowups{})
		at, _, err := f.outcomes.Admit(context.Background(), deploymentStartFor(dep, "gone", "gone"))
		if err != nil {
			t.Fatal(err)
		}
		var out Outcome
		if uncertain {
			out, err = f.outcomes.Uncertain(gone, at, errors.New("the effect's answer was lost"))
		} else {
			out, err = f.outcomes.Complete(gone, deploymentCompletionFor(at, "gone"))
		}
		if err != nil {
			t.Fatalf("uncertain=%v: settled for a gone caller: %s %v", uncertain, out, err)
		}
		got, err := NewDeployments(f.db).ByID(context.Background(), dep.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !uncertain && got.ObservedGeneration != at.Generation {
			t.Fatalf("the completed effect was not recorded: %+v", got)
		}
	}
}

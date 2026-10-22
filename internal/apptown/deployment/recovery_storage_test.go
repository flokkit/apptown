package deployment

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type recoveryJobFollowups struct{}

func (recoveryJobFollowups) EnqueueIn(ctx context.Context, tx *database.Tx, r jobs.Request) error {
	_, err := jobs.InsertOnTx(ctx, tx.Native(), "", r, 3, time.Time{})
	return err
}

func useRecoveryJobs(t *testing.T, f *outcomeFixture) {
	t.Helper()
	if err := jobs.Migrate(context.Background(), f.db.Pool()); err != nil {
		t.Fatal(err)
	}
	f.outcomes = NewOutcomes(f.db, recoveryJobFollowups{})
}

func failNextRecoveryJobInsert(t *testing.T, db *database.DB) {
	t.Helper()
	mustExec(t, db, `CREATE SEQUENCE IF NOT EXISTS fail_job_insert_sequence`)
	mustExec(t, db, `ALTER SEQUENCE fail_job_insert_sequence RESTART WITH 1`)
	mustExec(t, db, `CREATE OR REPLACE FUNCTION fail_job_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF nextval('fail_job_insert_sequence')=1 THEN RAISE EXCEPTION 'injected job insert failure'; END IF; RETURN NEW; END $$`)
	mustExec(t, db, `CREATE OR REPLACE TRIGGER fail_job_insert BEFORE INSERT ON river_job FOR EACH ROW EXECUTE FUNCTION fail_job_insert()`)
}

func TestDeploymentCheckpointIsAtomicGuardedAndImmutable(t *testing.T) {
	t.Parallel()
	f, dep, _, at := releaseOutcomeFixture(t)
	s := f.outcomes
	checkpoint := s.Checkpoint(context.Background(), at)
	values := map[string]any{"appId": "app-original", "deploymentId": "deployment-original"}
	dbtest.FailNextStatement(t, f.db, "UPDATE deployment_attempts SET observed_json")
	if err := checkpoint(values); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("checkpoint write: %v", err)
	}
	if a := storedAttempt(t, f.db, at.ID); string(a.ObservedJson) != "{}" {
		t.Fatalf("failed checkpoint left evidence: %s", a.ObservedJson)
	}
	if err := checkpoint(values); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint(values); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint(map[string]any{"appId": "another-app"}); err == nil {
		t.Fatal("checkpoint changed provider identity")
	}
	if err := checkpoint(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("checkpoint discarded an encoding error")
	}
	if _, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "next", "next")); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint(map[string]any{"late": "identity"}); err == nil {
		t.Fatal("late checkpoint modified a superseded attempt")
	}
	if got := observedOf(t, storedAttempt(t, f.db, at.ID)); !reflect.DeepEqual(got, values) {
		t.Fatalf("checkpoint: %v, want %v", got, values)
	}
}

func TestDeploymentRecoveryDiscoveryDistinguishesZeroAndFailure(t *testing.T) {
	t.Parallel()
	f, _ := deploymentOutcomeFixture(t)
	report, err := PrepareRecovery(context.Background(), f.outcomes)
	if err != nil || report.Queued != 0 {
		t.Fatalf("empty recovery: %+v %v", report, err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareRecovery(context.Background(), f.outcomes); err == nil {
		t.Fatalf("failed discovery was empty success: %v", err)
	}
}

func TestDeploymentAdmissionAndSettingsPreservePreparedTarget(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	useRecoveryJobs(t, f)
	firstTarget, err := NewTargets(f.db, nil).Put(context.Background(), &Target{Name: "prepared-first", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	secondTarget, err := NewTargets(f.db, nil).Put(context.Background(), &Target{Name: "prepared-second", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	first, second := firstTarget.ID, secondTarget.ID
	attrs := Attributes{Feed: "manual", Audience: "team", DataClass: "synthetic"}
	if _, err := NewDeployments(f.db).EnsureStanding(t.Context(), dep.ProjectID, dep.Name, first, attrs); err != nil {
		t.Fatal(err)
	}
	s := f.outcomes
	if _, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "stale", "selected")); err == nil {
		t.Fatal("admitted a release prepared for a different target")
	}
	dep, err = f.outcomes.DeploymentByID(context.Background(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	at, created, err := s.Admit(context.Background(), deploymentStartFor(dep, "current", "selected"))
	if err != nil || !created {
		t.Fatalf("current admission: %v %v", created, err)
	}
	if _, err := s.Uncertain(context.Background(), at, errors.New("interrupted")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDeployments(f.db).EnsureStanding(t.Context(), dep.ProjectID, dep.Name, second, attrs); err == nil {
		t.Fatal("settings moved an unresolved first generation")
	}
	attrs.DataClass = "production"
	if _, err := NewDeployments(f.db).EnsureStanding(t.Context(), dep.ProjectID, dep.Name, first, attrs); err == nil {
		t.Fatal("settings raised protection during unresolved deployment")
	}
	if d := storedDeployment(t, f.db, dep.ID); d.TargetID != first || d.DesiredGeneration != 1 || d.ObservedGeneration != 0 || d.Readiness != ReadinessUncertain {
		t.Fatalf("unresolved deployment moved: %+v", d)
	}
}

func TestDeploymentObservationPreservesSelectedImageAndRejectsUnencodableEvidence(t *testing.T) {
	t.Parallel()
	f, _, _, at := releaseOutcomeFixture(t)
	s := f.outcomes
	in := deploymentCompletionFor(at, "selected")
	in.Observed.ImageDigest = "wrong-image"
	if _, err := s.Complete(context.Background(), in); err == nil {
		t.Fatal("observation replaced the admitted image")
	}
	in.Observed.ImageDigest = "selected-image"
	in.Observed.Handle = map[string]any{"invalid": make(chan int)}
	if _, err := s.Complete(context.Background(), in); err == nil {
		t.Fatal("unencodable evidence became a completed deployment")
	}
	if a := storedAttempt(t, f.db, at.ID); a.Status != AttemptRunning.String() || string(a.ObservedJson) != "{}" {
		t.Fatalf("rejected observation was recorded: %s %s", a.Status, a.ObservedJson)
	}
	wantN(t, "release events", everyReleaseEvent(t, f.db), 1)
}

func TestDeploymentUncertaintyRacesAdmissionWithoutSplitOwnership(t *testing.T) {
	t.Parallel()
	f, dep, releaseID, at := releaseOutcomeFixture(t)
	useRecoveryJobs(t, f)
	s := f.outcomes
	results := race(2, func(i int) int {
		if i == 0 {
			_, created, err := s.Admit(context.Background(), deploymentStartFor(dep, "next", "next"))
			if errors.Is(err, ErrUncertain) {
				return 0
			}
			if err != nil || !created {
				t.Errorf("admission: %v %v", created, err)
				return -1
			}
			return 1
		}
		out, err := s.Uncertain(context.Background(), at, context.DeadlineExceeded)
		if err != nil {
			t.Error(err)
			return -1
		}
		if out == Uncertain {
			return 2
		}
		if out == Superseded {
			return 0
		}
		t.Errorf("uncertainty: %s", out)
		return -1
	})
	if count(results, 1) == 1 {
		if a := storedAttempt(t, f.db, at.ID); a.Status != AttemptSuperseded.String() {
			t.Fatalf("superseded attempt: %s", a.Status)
		}
		if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State != ReleaseFailed {
			t.Fatalf("superseded release: %s", r.State)
		}
		wantN(t, "recovery jobs", queuedJobs(t, f.db, RecoveryJob, ""), 0)
	} else if count(results, 2) == 1 {
		wantCount(t, "uncertain attempts", attemptsWhere(t, f.db, func(a deploymentsql.DeploymentAttempt) bool { return a.DeploymentID == dep.ID }, AttemptUncertain), 1)
		if d := storedDeployment(t, f.db, dep.ID); d.DesiredGeneration != 1 || d.Readiness != ReadinessUncertain {
			t.Fatalf("uncertain deployment: desired %d %s", d.DesiredGeneration, d.Readiness)
		}
		wantN(t, "recovery jobs", queuedJobs(t, f.db, RecoveryJob, ""), 1)
	} else {
		t.Fatalf("neither operation owned the deployment: %v", results)
	}
}

func TestDeploymentRecoveryMissingAttemptDiffersFromFailedRead(t *testing.T) {
	t.Parallel()
	f, _ := deploymentOutcomeFixture(t)
	r := Recoverer{Source: RecoverySourceFor(f.outcomes, nil)}
	result, err := r.Run(context.Background(), idstest.N(999))
	if err != nil || result.Outcome != "absent" {
		t.Fatalf("missing attempt: %+v %v", result, err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err = r.Run(context.Background(), idstest.N(999))
	if err == nil || result.Outcome == "absent" {
		t.Fatalf("read failure became absence: %+v %v", result, err)
	}
}

type storedRecoveryDriver struct {
	deploy.Driver
	resume func(context.Context, deploy.Placement, int64, map[string]any) (deploy.Observation, error)
}

func (d storedRecoveryDriver) Resume(ctx context.Context, placement deploy.Placement, generation int64, checkpoint map[string]any, _ func(string, ...any)) (deploy.Observation, error) {
	return d.resume(ctx, placement, generation, checkpoint)
}

func TestDeploymentUncertaintyAndObservationQueueCommitTogether(t *testing.T) {
	t.Parallel()
	for _, statement := range []string{"-- name: UncertainAttempt", "-- name: SetReadiness", "INSERT INTO jobs"} {
		t.Run(statement, func(t *testing.T) {
			f, dep, releaseID, at := releaseOutcomeFixture(t)
			useRecoveryJobs(t, f)
			s := f.outcomes
			if err := s.Checkpoint(context.Background(), at)(map[string]any{"appId": "recorded-app", "deploymentId": "recorded-deployment"}); err != nil {
				t.Fatal(err)
			}
			if statement == "INSERT INTO jobs" {
				failNextRecoveryJobInsert(t, f.db)
			} else {
				dbtest.FailNextStatement(t, f.db, statement)
			}
			if _, err := s.Uncertain(context.Background(), at, context.DeadlineExceeded); err == nil || (statement != "INSERT INTO jobs" && !errors.Is(err, database.ErrInjectedFailure)) {
				t.Fatalf("uncertainty write: %v", err)
			}
			// The key's presence, not its value: a "_recovery": null left behind is still a recovery mark.
			if a := storedAttempt(t, f.db, at.ID); a.Status != AttemptRunning.String() || hasObservedKey(t, a, "_recovery") {
				t.Fatalf("rolled-back uncertainty left the attempt %s %s", a.Status, a.ObservedJson)
			}
			if d := storedDeployment(t, f.db, dep.ID); d.Readiness != Reconciling {
				t.Fatalf("rolled-back uncertainty left the deployment %s", d.Readiness)
			}
			wantN(t, "queued jobs", queuedJobs(t, f.db, "", ""), 0)
			for i := 0; i < 2; i++ {
				if out, err := s.Uncertain(context.Background(), at, context.DeadlineExceeded); err != nil || out != Uncertain {
					t.Fatalf("uncertain outcome: %s %v", out, err)
				}
			}
			wantN(t, "available recovery jobs", queuedJobs(t, f.db, RecoveryJob, "available"), 1)
			if a := storedAttempt(t, f.db, at.ID); a.Status != AttemptUncertain.String() || a.Terminal || a.FinishedAt.Valid || observedOf(t, a)["appId"] != "recorded-app" {
				t.Fatalf("uncertain attempt: %+v", a)
			}
			if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State != Releasing {
				t.Fatalf("release under uncertainty: %s", r.State)
			}
			if _, _, err := s.Admit(context.Background(), deploymentStartFor(dep, "next", "next")); !errors.Is(err, ErrUncertain) {
				t.Fatalf("conflicting admission: %v", err)
			}
			if out, err := s.Complete(context.Background(), deploymentCompletionFor(at, "recovered")); err != nil || out != Applied {
				t.Fatalf("settling uncertainty: %s %v", out, err)
			}
			if _, created, err := s.Admit(context.Background(), deploymentStartFor(dep, "next", "next")); err != nil || !created {
				t.Fatalf("admission after resolution: %v %v", created, err)
			}
		})
	}
}

func TestDeploymentRecoveryClassifiesObservationAndReplaysWithoutProvider(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                      string
		cause                     error
		outcome, attempt, release string
	}{
		{"recovered", nil, "recovered", "done", "released"},
		{"rejected", deploy.Terminal(errors.New("provider confirmed ERROR")), "failed", "failed", "failed"},
		{"timeout", context.DeadlineExceeded, "uncertain", "uncertain", "releasing"},
		{"transport", errors.New("connection reset"), "uncertain", "uncertain", "releasing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, dep, releaseID, at := releaseOutcomeFixture(t)
			useRecoveryJobs(t, f)
			s := f.outcomes
			if err := s.Checkpoint(context.Background(), at)(map[string]any{"providerId": "original"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Uncertain(context.Background(), at, errors.New("restart")); err != nil {
				t.Fatal(err)
			}
			calls := 0
			r := Recoverer{Store: s, Source: RecoverySourceFor(s, func(context.Context, *Deployment) (deploy.Driver, error) {
				return storedRecoveryDriver{resume: func(ctx context.Context, placement deploy.Placement, generation int64, checkpoint map[string]any) (deploy.Observation, error) {
					calls++
					if generation != at.Generation || checkpoint["providerId"] != "original" {
						t.Fatal("recovery lost attempt identity")
					}
					return deploymentCompletionFor(at, "confirmed").Observed, tc.cause
				}}, nil
			})}
			result, err := r.Run(context.Background(), at.ID)
			if result.Outcome != tc.outcome || (err != nil) != (tc.outcome == "uncertain") {
				t.Fatalf("observation: %+v %v", result, err)
			}
			if a := storedAttempt(t, f.db, at.ID); a.Status != tc.attempt {
				t.Fatalf("attempt: %s, want %s", a.Status, tc.attempt)
			}
			if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State.String() != tc.release {
				t.Fatalf("release: %s, want %s", r.State, tc.release)
			}
			if tc.outcome != "uncertain" {
				if _, err := r.Run(context.Background(), at.ID); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatal("replay after settlement called the provider")
				}
				wantN(t, "release events", releaseEvents(t, f.db, releaseID), 2)
			}
		})
	}
}

func TestDeploymentRecoveryCancellationDoesNotPublishSuccess(t *testing.T) {
	t.Parallel()
	f, _, _, at := releaseOutcomeFixture(t)
	useRecoveryJobs(t, f)
	s := f.outcomes
	if _, err := s.Uncertain(context.Background(), at, errors.New("restart")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := Recoverer{Store: s, Source: RecoverySourceFor(s, func(context.Context, *Deployment) (deploy.Driver, error) {
		return storedRecoveryDriver{resume: func(context.Context, deploy.Placement, int64, map[string]any) (deploy.Observation, error) {
			cancel()
			return deploymentCompletionFor(at, "late").Observed, nil
		}}, nil
	})}
	if _, err := r.Run(ctx, at.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recovery: %v", err)
	}
	if a := storedAttempt(t, f.db, at.ID); a.Status != AttemptUncertain.String() {
		t.Fatalf("cancelled recovery settled the attempt: %s", a.Status)
	}
	wantN(t, "release events", everyReleaseEvent(t, f.db), 1)
}

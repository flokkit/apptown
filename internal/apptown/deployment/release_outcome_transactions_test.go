package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

func releaseOutcomeFixture(t *testing.T) (*outcomeFixture, *Deployment, ids.ID, Attempt) {
	t.Helper()
	f, dep := deploymentOutcomeFixture(t)
	cut, err := NewReleases(f.db).Start(context.Background(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Deployment: "main", Revision: "selected-revision", Actor: "alice"}, "prepared for settlement")
	if err != nil {
		t.Fatal(err)
	}
	at, _, err := f.outcomes.Admit(context.Background(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: fmt.Sprintf("release-%d", cut.ID), Inputs: Inputs{ReleaseID: cut.ID, Revision: "selected-revision", ImageDigest: "selected-image", ArtifactHash: "selected-artifact"}})
	if err != nil {
		t.Fatal(err)
	}
	return f, dep, cut.ID, at
}

// assertReleaseUnsettled holds the fixture to what admission left: the
// deployment unobserved, the attempt open, the release releasing with only
// its start event (the only event on the instance), and no receipt.
func assertReleaseUnsettled(t *testing.T, f *outcomeFixture, dep *Deployment, releaseID ids.ID, at Attempt) {
	t.Helper()
	if d := storedDeployment(t, f.db, dep.ID); d.ObservedGeneration != 0 || d.Readiness != Reconciling {
		t.Fatalf("deployment settled: observed %d %s", d.ObservedGeneration, d.Readiness)
	}
	if a := storedAttempt(t, f.db, at.ID); a.Status != AttemptRunning.String() || string(a.ObservedJson) != "{}" || a.FinishedAt.Valid {
		t.Fatalf("attempt settled: %s %s finished=%v", a.Status, a.ObservedJson, a.FinishedAt.Valid)
	}
	if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State != Releasing || r.ReleasedAt.Valid || r.Generation != 0 || r.ExternalRef != "" {
		t.Fatalf("release settled: %+v", r)
	}
	wantN(t, "release events", everyReleaseEvent(t, f.db), 1)
	wantN(t, "receipts", receipts(t, f.db, ""), 0)
}

// assertPreparationUnsettled holds the instance's only release to releasing
// with only its start event.
func assertPreparationUnsettled(t *testing.T, f *outcomeFixture, releaseID ids.ID) {
	t.Helper()
	rels := everyRelease(t, f.db)
	if len(rels) != 1 || rels[0].ID != releaseID || rels[0].State != Releasing {
		t.Fatalf("releases after a refused preparation failure: %+v", rels)
	}
	wantN(t, "release events", everyReleaseEvent(t, f.db), 1)
}

// releasesCarrying is every release on the instance that shipped this image and artifact.
func releasesCarrying(t *testing.T, db *database.DB, image, artifact string) []*Release {
	t.Helper()
	var out []*Release
	for _, r := range everyRelease(t, db) {
		if r.ExternalRef == image && r.ArtifactHash == artifact {
			out = append(out, r)
		}
	}
	return out
}

func TestDeploymentReleaseRollsBackEveryCompletionWrite(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		for _, statement := range []string{"UPDATE deployments", "-- name: FinishAttempt", "UPDATE releases SET state =", "INSERT INTO release_events", "INSERT INTO audit_log"} {
			t.Run(fmt.Sprint(failure, statement), func(t *testing.T) {
				f, dep, releaseID, at := releaseOutcomeFixture(t)
				dbtest.FailNextStatement(t, f.db, statement)
				var err error
				if failure {
					_, err = f.outcomes.Fail(context.Background(), at, deploy.Terminal(errors.New("provider refused")))
				} else {
					_, err = f.outcomes.Complete(context.Background(), deploymentCompletionFor(at, "selected"))
				}
				if !errors.Is(err, database.ErrInjectedFailure) {
					t.Fatalf("outcome did not propagate write failure: %v", err)
				}
				assertReleaseUnsettled(t, f, dep, releaseID, at)
				if out, err := f.outcomes.Complete(context.Background(), deploymentCompletionFor(at, "retry")); err != nil || out != Applied {
					t.Fatalf("retry after rollback: %s %v", out, err)
				}
				wantN(t, "releases carrying the selected image", releasesCarrying(t, f.db, "selected-image", "selected-artifact"), 1)
			})
		}
	}
}

func TestDeploymentReleaseConcurrentReplayHasOneReceipt(t *testing.T) {
	t.Parallel()
	f, _, releaseID, at := releaseOutcomeFixture(t)
	results := race(8, func(i int) int {
		out, err := f.outcomes.Complete(context.Background(), deploymentCompletionFor(at, fmt.Sprint("response-", i)))
		if err != nil {
			t.Error(err)
			return -1
		}
		if out == Applied {
			return 1
		}
		return 0
	})
	if count(results, 1) != 1 {
		t.Fatalf("completion winners: %v", results)
	}
	wantN(t, "release events", releaseEvents(t, f.db, releaseID), 2)
	wantN(t, "release.deployed receipts", receipts(t, f.db, "release.deployed"), 1)
	wantN(t, "releases carrying the selected image", releasesCarrying(t, f.db, "selected-image", "selected-artifact"), 1)
}

func TestDeploymentReleaseSupersessionSettlesOldReleaseAtomically(t *testing.T) {
	t.Parallel()
	f, dep, releaseID, old := releaseOutcomeFixture(t)
	s := f.outcomes
	start := deploymentStartFor(dep, "next", "next")
	dbtest.FailNextStatement(t, f.db, "INSERT INTO release_events")
	if _, _, err := s.Admit(context.Background(), start); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("supersession receipt failure: %v", err)
	}
	if a := storedAttempt(t, f.db, old.ID); a.Status != AttemptRunning.String() || !a.SupersededBy.IsZero() {
		t.Fatalf("old attempt after rolled-back supersession: %+v", a)
	}
	if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State != Releasing {
		t.Fatalf("release after rolled-back supersession: %s", r.State)
	}
	// sql-guarantee: the rolled-back admission must leave no inputs row behind; no owner read lists inputs without their attempt.
	dbtest.Count(t, f.db, 1, `SELECT COUNT(*) FROM deployment_attempt_inputs`)
	if d := storedDeployment(t, f.db, dep.ID); d.DesiredGeneration != 1 {
		t.Fatalf("desired generation after rolled-back supersession: %d", d.DesiredGeneration)
	}
	wantCount(t, "deployments at desired generation 1", deploymentsWhere(t, f.db, func(d *Deployment) bool { return d.DesiredGeneration == 1 }), 1)
	next, _, err := s.Admit(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State != ReleaseFailed || r.Generation != old.Generation {
		t.Fatalf("superseded release: %s generation %d, want failed at %d", r.State, r.Generation, old.Generation)
	}
	superseded := 0
	for _, e := range releaseEvents(t, f.db, releaseID) {
		if strings.Contains(e.Text, "superseded") {
			superseded++
		}
	}
	if superseded != 1 {
		t.Fatalf("supersession events: %d, want 1", superseded)
	}
	if out, err := s.Complete(context.Background(), deploymentCompletionFor(old, "late")); err != nil || out != Superseded {
		t.Fatalf("late completion: %s %v", out, err)
	}
	wantN(t, "release events", releaseEvents(t, f.db, releaseID), 2)
	if d := storedDeployment(t, f.db, dep.ID); d.DesiredGeneration != next.Generation || d.ObservedGeneration != 0 || d.Readiness != Reconciling {
		t.Fatalf("deployment after late completion: desired %d observed %d %s", d.DesiredGeneration, d.ObservedGeneration, d.Readiness)
	}
	wantCount(t, "deployments reconciling the next generation", deploymentsWhere(t, f.db, func(d *Deployment) bool {
		return d.DesiredGeneration == next.Generation && d.ObservedGeneration == 0 && d.Readiness == Reconciling
	}), 1)
}

func TestDeploymentReleasePreparationFailureCannotBypassAdmission(t *testing.T) {
	t.Parallel()
	f, dep, releaseID, at := releaseOutcomeFixture(t)
	if _, err := f.outcomes.FailUnstartedRelease(t.Context(), releaseID, errors.New("late preparation")); err == nil {
		t.Fatal("preparation failure bypassed an admitted attempt")
	}
	if a := storedAttempt(t, f.db, at.ID); a.Status != AttemptRunning.String() {
		t.Fatalf("attempt: %s", a.Status)
	}
	if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State != Releasing {
		t.Fatalf("release: %s", r.State)
	}
}

func TestDeploymentReleasePreparationFailureHasOneReceipt(t *testing.T) {
	t.Parallel()
	f, dep := deploymentOutcomeFixture(t)
	cut, err := NewReleases(f.db).Start(context.Background(), ReleaseDraft{Source: ReleaseFromBundle, ProjectID: dep.ProjectID, DeploymentID: dep.ID, Deployment: "main", Revision: "selected-revision", Actor: "alice"}, "prepared for settlement")
	if err != nil {
		t.Fatal(err)
	}
	releaseID, s := cut.ID, f.outcomes
	// An UPDATE can succeed but affect no row (for example a trigger veto).
	// Generated :execrows must prevent committing a receipt for that non-write.
	mustExec(t, f.db, `CREATE FUNCTION veto_release_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
	mustExec(t, f.db, `CREATE TRIGGER veto_release_update BEFORE UPDATE ON releases FOR EACH ROW EXECUTE FUNCTION veto_release_update()`)
	if _, err := s.FailUnstartedRelease(t.Context(), releaseID, errors.New("preparation")); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("zero-row write reported success: %v", err)
	}
	assertPreparationUnsettled(t, f, releaseID)
	mustExec(t, f.db, `DROP TRIGGER veto_release_update ON releases`)
	mustExec(t, f.db, `DROP FUNCTION veto_release_update()`)
	dbtest.FailNextStatement(t, f.db, "INSERT INTO audit_log")
	if _, err := s.FailUnstartedRelease(t.Context(), releaseID, errors.New("preparation")); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("preparation audit failure: %v", err)
	}
	assertPreparationUnsettled(t, f, releaseID)
	for _, want := range []Outcome{Applied, AlreadyApplied} {
		if out, err := s.FailUnstartedRelease(t.Context(), releaseID, errors.New("preparation")); err != nil || out != want {
			t.Fatalf("preparation outcome: %s %v, want %s", out, err, want)
		}
	}
	wantN(t, "release events", everyReleaseEvent(t, f.db), 2)
	wantN(t, "receipts", receipts(t, f.db, ""), 1)
}

func TestDeploymentReleaseCompletionSettlesAllRecords(t *testing.T) {
	t.Parallel()
	f, dep, releaseID, at := releaseOutcomeFixture(t)
	in := deploymentCompletionFor(at, "selected")
	if out, err := f.outcomes.Complete(context.Background(), in); err != nil || out != Applied {
		t.Fatalf("completion: %s %v", out, err)
	}
	if r := storedRelease(t, f.db, dep.ProjectID, releaseID); r.State != Released || r.Generation != at.Generation || r.ExternalRef != "selected-image" || !r.ReleasedAt.Valid {
		t.Fatalf("settled release: %+v", r)
	}
	if d := storedDeployment(t, f.db, dep.ID); d.ObservedGeneration != at.Generation || d.ReleaseSHA != "selected-revision" {
		t.Fatalf("settled deployment: observed %d revision %q", d.ObservedGeneration, d.ReleaseSHA)
	}
	wantN(t, "release events", releaseEvents(t, f.db, releaseID), 2)
	wantN(t, "release.deployed receipts by alice", receipts(t, f.db, "release.deployed", byActor("alice")), 1)
}

func TestDeploymentReleaseRequiresEverySettlementRecord(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		for _, table := range []string{"releases", "release_events", "audit_log"} {
			t.Run(fmt.Sprint(failed, table), func(t *testing.T) {
				f, dep, releaseID, at := releaseOutcomeFixture(t)
				operation := "INSERT"
				if table == "releases" {
					operation = "UPDATE"
				}
				mustExec(t, f.db, `CREATE FUNCTION veto_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$`)
				mustExec(t, f.db, `CREATE TRIGGER veto_settlement BEFORE `+operation+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION veto_settlement()`)
				settle := func() (Outcome, error) {
					if failed {
						return f.outcomes.Fail(context.Background(), at, deploy.Terminal(errors.New("provider refused")))
					}
					return f.outcomes.Complete(context.Background(), deploymentCompletionFor(at, "selected"))
				}
				if out, err := settle(); err == nil {
					t.Fatalf("missing %s committed as %s", table, out)
				}
				assertReleaseUnsettled(t, f, dep, releaseID, at)
				mustExec(t, f.db, `DROP TRIGGER veto_settlement ON `+table)
				if out, err := settle(); err != nil || out != Applied {
					t.Fatalf("retry: %s %v", out, err)
				}
				wantN(t, "release events", everyReleaseEvent(t, f.db), 2)
				wantN(t, "receipts", receipts(t, f.db, ""), 1)
			})
		}
	}
}

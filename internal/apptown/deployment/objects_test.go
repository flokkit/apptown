package deployment

import (
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Pure rules, no database: an attempt settles against the deployment it
// belongs to.
func TestAttemptSettle(t *testing.T) {
	t.Parallel()
	dep := &Deployment{ID: idstest.N(1), DesiredGeneration: 3, Readiness: reconciling}
	at := Attempt{ID: idstest.N(10), DeploymentID: idstest.N(1), Generation: 3}
	v, err := at.settle(dep, AttemptRunning, false)
	if err != nil || v.Outcome != Applied || v.Next != AttemptDone || !v.Changed || v.Readiness != Reconciled {
		t.Fatalf("a running attempt of the desired generation applies: %+v %v", v, err)
	}
	v, err = at.settle(dep, AttemptRunning, true)
	if err != nil || v.Outcome != Applied || v.Next != AttemptFailed || !v.Changed || v.Readiness != ReadinessFailed {
		t.Fatalf("a failed attempt settles failed and degrades: %+v %v", v, err)
	}
	if v, err := at.settle(dep, AttemptDone, false); err != nil || v.Outcome != alreadyApplied || v.Changed {
		t.Fatalf("the recorded outcome replayed is already applied: %+v %v", v, err)
	}
	if _, err := at.settle(dep, AttemptDone, true); !errors.Is(err, errOutcomeConflict) {
		t.Fatalf("a different outcome for a settled attempt conflicts: %v", err)
	}
	moved := &Deployment{ID: idstest.N(1), DesiredGeneration: 4, Readiness: reconciling}
	v, err = at.settle(moved, AttemptRunning, false)
	if err != nil || v.Outcome != Superseded || v.Next != attemptSuperseded || !v.Changed {
		t.Fatalf("an open attempt of an old generation closes superseded: %+v %v", v, err)
	}
	if v, err := at.settle(moved, AttemptDone, false); err != nil || v.Outcome != Superseded || v.Changed {
		t.Fatalf("a closed attempt of an old generation is superseded without a write: %+v %v", v, err)
	}
}

func TestAttemptUnsettled(t *testing.T) {
	t.Parallel()
	dep := &Deployment{ID: idstest.N(1), DesiredGeneration: 2, Readiness: reconciling}
	at := Attempt{ID: idstest.N(5), DeploymentID: idstest.N(1), Generation: 2}
	v, err := at.unsettled(dep, AttemptRunning)
	if err != nil || v.Outcome != outcomeUncertain || v.Next != AttemptUncertain || !v.Changed || v.Readiness != ReadinessUncertain {
		t.Fatalf("a running attempt becomes uncertain and readiness is lost: %+v %v", v, err)
	}
	if v, err := at.unsettled(dep, AttemptUncertain); err != nil || v.Outcome != outcomeUncertain || v.Next != AttemptUncertain {
		t.Fatalf("uncertainty is recorded once: %+v %v", v, err)
	}
	if v, err := at.unsettled(dep, AttemptDone); err != nil || v.Outcome != alreadyApplied || v.Changed {
		t.Fatalf("a closed attempt stays closed: %+v %v", v, err)
	}
	if v, err := at.unsettled(&Deployment{DesiredGeneration: 9}, AttemptRunning); err != nil || v.Outcome != Superseded || v.Changed {
		t.Fatalf("an old generation is superseded: %+v %v", v, err)
	}
	if err := at.completes(deploy.Observation{Generation: 2, Rollout: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := at.completes(deploy.Observation{Generation: 1, Rollout: "active"}); err == nil {
		t.Fatal("another generation's observation does not complete the attempt")
	}
}

func TestDeploymentAdmit(t *testing.T) {
	t.Parallel()
	dep := &Deployment{ID: idstest.N(7), TargetID: idstest.N(2), DesiredGeneration: 4, Readiness: Reconciled}
	if err := dep.bound(idstest.N(3)); err == nil {
		t.Fatal("a changed target binding is refused")
	}
	gen, readiness, err := dep.admit(nil)
	if err != nil || gen != 5 || readiness != reconciling {
		t.Fatalf("admission is the next generation, on its way: %d %s %v", gen, readiness, err)
	}
	four := int64(4)
	if _, _, err := dep.admit(&four); err != nil {
		t.Fatalf("a rebuild of the selected generation is admitted: %v", err)
	}
	three := int64(3)
	if _, _, err := dep.admit(&three); err == nil {
		t.Fatal("a rebuild of a generation the deployment moved past is refused")
	}
}

func TestTargetRetire(t *testing.T) {
	t.Parallel()
	own := &Target{Name: "built-in-docker", Origin: OriginBuiltIn, Status: TargetReady}
	if _, err := own.retire(""); !errors.Is(err, errTargetConflict) {
		t.Fatalf("AppTown's own host is never retired: %v", err)
	}
	t1 := &Target{Name: "cloud", Origin: OriginAttached, Status: TargetReady}
	if _, err := t1.retire("app-main"); !errors.Is(err, errTargetConflict) {
		t.Fatalf("bound deployments refuse retirement: %v", err)
	}
	next, err := t1.retire("")
	if err != nil || next != TargetRetired {
		t.Fatalf("a free target retires: %s %v", next, err)
	}
	gone := &Target{Name: "old", Origin: OriginAttached, Status: TargetRetired}
	if _, err := gone.retire(""); !errors.Is(err, ErrTargetRetired) {
		t.Fatalf("already retired is nothing to do: %v", err)
	}
}

package tasks

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
)

// attemptFixture makes the run and step a task attempt belongs to.
func attemptFixture(t *testing.T) (*Records, Admission) {
	t.Helper()
	db := testDB(t)
	projectID, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "tasks", Name: "Tasks"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var runID, stepID ids.ID
	// sql-guarantee: create the sparse run and step a task attempt references; importing Workflow's admission would give this lower owner a dependency on its caller.
	if err := db.SQL().QueryRowContext(context.Background(), `INSERT INTO runs(id,number,project_id,workflow_name,workflow_version,kind,current_stage,definition_json,created_at,updated_at) VALUES ($1,1000,$2,'fixture',1,'pipeline','build','{}','2026-10-09T00:00:00Z','2026-10-09T00:00:00Z') RETURNING id`, ids.New(time.Now()), projectID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: see above.
	if err := db.SQL().QueryRowContext(context.Background(), `INSERT INTO steps(id,run_id,stage,name,type,status) VALUES ($1,$2,'build','make','script','running') RETURNING id`, ids.New(time.Now()), runID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	return New(db), Admission{Operation: "step-1-attempt-1", Invocation: ScriptInvocation, Owner: Owner{ProjectID: projectID, RunID: runID, StepID: stepID, Attempt: 1},
		Backend: "docker", Container: "apptown-task-step-1-attempt-1", Digest: "digest-a",
		Admitted: execution.Admission{Profile: "docker", Network: execution.NetworkNone, Limits: execution.Limits{CPUs: 1, WallClock: time.Minute}, Expires: time.Now().Add(time.Hour).Round(time.Microsecond)},
		Deadline: time.Now().Add(time.Minute).Round(time.Microsecond)}
}

func TestReservingAnOperationAgainFindsTheSameAttempt(t *testing.T) {
	r, in := attemptFixture(t)
	ctx := context.Background()
	first, err := r.Reserve(ctx, in)
	if err != nil || first.Observed != Reserved {
		t.Fatalf("reserve: %+v %v", first, err)
	}
	again, err := r.Reserve(ctx, in)
	if err != nil || again.Operation != first.Operation || again.Observed != Reserved {
		t.Fatalf("a retried reserve: %+v %v", again, err)
	}
	changed := in
	changed.Digest = "digest-b"
	if _, err := r.Reserve(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("the same key with a different request = %v, want %v", err, ErrConflict)
	}
}

func TestAnAttemptHasOneProviderIdentityAndSettlesOnce(t *testing.T) {
	r, in := attemptFixture(t)
	ctx := context.Background()
	if _, err := r.Reserve(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordProvider(ctx, in.Operation, "container-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordProvider(ctx, in.Operation, "container-1"); err != nil {
		t.Fatalf("recording the same identity again: %v", err)
	}
	if err := r.RecordProvider(ctx, in.Operation, "container-2"); err == nil {
		t.Fatal("a second provider identity was recorded for one attempt")
	}
	code := 0
	if err := r.Settle(ctx, in.Operation, &code, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordUnknown(ctx, in.Operation, "late"); !errors.Is(err, ErrSettled) {
		t.Fatalf("observing a settled attempt = %v, want %v", err, ErrSettled)
	}
	if err := r.RequestStop(ctx, in.Operation); !errors.Is(err, ErrSettled) {
		t.Fatalf("stopping a settled attempt = %v, want %v", err, ErrSettled)
	}
	left, err := r.Unsettled(ctx)
	if err != nil || len(left) != 0 {
		t.Fatalf("unsettled after settling: %+v %v", left, err)
	}
	got, err := r.Get(ctx, in.Operation)
	if err != nil || got.Observed != Settled || got.ExitCode == nil || *got.ExitCode != 0 || got.ProviderID != "container-1" {
		t.Fatalf("the settled receipt: %+v %v", got, err)
	}
}

func TestAStopIsRecordedDurably(t *testing.T) {
	r, in := attemptFixture(t)
	ctx := context.Background()
	if _, err := r.Reserve(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := r.RequestStop(ctx, in.Operation); err != nil {
		t.Fatal(err)
	}
	steps, err := UnsettledStepsIn(ctx, r.db)
	if err != nil || len(steps) != 1 || steps[0] != in.Owner.StepID {
		t.Fatalf("steps with unsettled tasks: %v %v", steps, err)
	}
	left, err := r.Unsettled(ctx)
	if err != nil || len(left) != 1 || !left[0].StopWanted {
		t.Fatalf("a recorded stop: %+v %v", left, err)
	}
}

// A release build's step is not preserved by the startup repair: its
// unsettled task does not keep its step running, while a script's does.
func TestAReleaseBuildDoesNotKeepItsStepRunningAcrossARestart(t *testing.T) {
	r, in := attemptFixture(t)
	ctx := context.Background()
	build := in
	build.Operation, build.Invocation = "step-1-attempt-1-release-build", ReleaseBuildInvocation
	if _, err := r.Reserve(ctx, build); err != nil {
		t.Fatal(err)
	}
	if steps, err := UnsettledStepsIn(ctx, r.db); err != nil || len(steps) != 0 {
		t.Fatalf("a release build preserved its step: %v %v", steps, err)
	}
	if left, err := r.Unsettled(ctx); err != nil || len(left) != 1 || left[0].Operation != build.Operation {
		t.Fatalf("the release build is still for the task reconciler to settle: %+v %v", left, err)
	}
	if _, err := r.Reserve(ctx, in); err != nil {
		t.Fatal(err)
	}
	if steps, err := UnsettledStepsIn(ctx, r.db); err != nil || len(steps) != 1 || steps[0] != in.Owner.StepID {
		t.Fatalf("a script's task no longer preserves its step: %v %v", steps, err)
	}
}

// An attempt may run several invocations: each is its own row under its own
// operation, and one invocation of an attempt is one row. Reserving the same
// invocation under another operation is refused, and so is a reservation
// without an invocation.
func TestEachInvocationOfAnAttemptIsOneRow(t *testing.T) {
	r, in := attemptFixture(t)
	ctx := context.Background()
	agent, retry := in, in
	agent.Operation, agent.Invocation, agent.Workspace = "step-1-a1-agent", "agent", "step-1-a1"
	retry.Operation, retry.Invocation, retry.Workspace = "step-1-a1-agent-retry", "agent-retry", "step-1-a1"
	for _, a := range []Admission{agent, retry} {
		if _, err := r.Reserve(ctx, a); err != nil {
			t.Fatalf("reserving %s: %v", a.Invocation, err)
		}
	}
	for _, a := range []Admission{agent, retry} {
		got, err := r.Get(ctx, a.Operation)
		if err != nil || got.Invocation != a.Invocation || got.Workspace != "step-1-a1" || got.Owner != in.Owner {
			t.Fatalf("the %s invocation: %+v %v", a.Invocation, got, err)
		}
	}
	again := agent
	again.Operation = "step-1-a1-agent-again"
	if _, err := r.Reserve(ctx, again); !errors.Is(err, ErrInvocationTaken) {
		t.Fatalf("a second row for one invocation = %v, want %v", err, ErrInvocationTaken)
	}
	if _, err := r.Get(ctx, again.Operation); err == nil {
		t.Fatal("the refused reservation was recorded")
	}
	unnamed := in
	unnamed.Operation, unnamed.Invocation = "step-1-a1-unnamed", ""
	if _, err := r.Reserve(ctx, unnamed); err == nil {
		t.Fatal("a reservation without an invocation was accepted")
	}
}

// A settled attempt is released only when the backend confirms it: until
// then it is listed for another try on its own backend, and afterwards not
// again. An attempt of which nothing is held is settled released in one
// write. Only a settled attempt is released.
func TestAReleaseIsRecordedOnlyWhenConfirmed(t *testing.T) {
	r, in := attemptFixture(t)
	ctx := context.Background()
	other := in
	other.Operation, other.Invocation = "step-1-attempt-1-other", "other"
	refused := in
	refused.Operation, refused.Invocation = "step-1-attempt-1-refused", "refused"
	for _, a := range []Admission{in, other, refused} {
		if _, err := r.Reserve(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ConfirmRelease(ctx, in.Operation); err == nil {
		t.Fatal("an unsettled attempt was recorded released")
	}
	code := 0
	for _, op := range []string{in.Operation, other.Operation} {
		if err := r.Settle(ctx, op, &code, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SettleNothingHeld(ctx, refused.Operation, "refused"); err != nil {
		t.Fatal(err)
	}
	pending := func(backend string) []string {
		t.Helper()
		list, err := r.Unreleased(ctx, backend, 10)
		if err != nil {
			t.Fatal(err)
		}
		var ops []string
		for _, a := range list {
			ops = append(ops, a.Operation)
		}
		return ops
	}
	if got := pending(in.Backend); len(got) != 2 || !slices.Contains(got, in.Operation) || !slices.Contains(got, other.Operation) {
		t.Fatalf("settled, unreleased: %v", got)
	}
	if got := pending("another backend"); len(got) != 0 {
		t.Fatalf("another backend's list: %v", got)
	}
	if err := r.ConfirmRelease(ctx, in.Operation); err != nil {
		t.Fatal(err)
	}
	if err := r.ConfirmRelease(ctx, in.Operation); err != nil {
		t.Fatalf("confirming again: %v", err)
	}
	if got := pending(in.Backend); len(got) != 1 || got[0] != other.Operation {
		t.Fatalf("after one release: %v", got)
	}
	for _, op := range []string{in.Operation, refused.Operation} {
		if got, err := r.Get(ctx, op); err != nil || !got.Released || got.Observed != Settled {
			t.Fatalf("%s: %+v %v", op, got, err)
		}
	}
}

// A shared workspace is ready for release, and its release is recorded,
// only once every invocation naming it is settled and released; an
// attempt's invocations are listed in the order they were reserved.
func TestAWorkspaceIsReleasedOnlyAfterEveryInvocationInIt(t *testing.T) {
	r, in := attemptFixture(t)
	ctx := context.Background()
	first := in
	first.Operation, first.Invocation, first.Workspace, first.Container = "step-1-attempt-1-agent", "agent", "step-1-a1", "step-1-attempt-1-agent"
	second := first
	second.Operation, second.Invocation, second.Container = "step-1-attempt-1-agent-retry", "agent-retry", "step-1-attempt-1-agent-retry"
	for _, a := range []Admission{first, second} {
		if _, err := r.Reserve(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := r.Invocations(ctx, in.Owner.StepID, in.Owner.Attempt)
	if err != nil || len(listed) != 2 || listed[0].Invocation != "agent" || listed[1].Invocation != "agent-retry" {
		t.Fatalf("invocations: %+v %v", listed, err)
	}
	ready := func() []string {
		t.Helper()
		got, err := r.UnreleasedWorkspaces(ctx, in.Backend, 10)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if err := r.Settle(ctx, first.Operation, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.ConfirmRelease(ctx, first.Operation); err != nil {
		t.Fatal(err)
	}
	if got := ready(); len(got) != 0 {
		t.Fatalf("a workspace with an unsettled invocation is ready: %v", got)
	}
	if err := r.ConfirmWorkspaceRelease(ctx, in.Backend, first.Workspace); !errors.Is(err, ErrWorkspaceInUse) {
		t.Fatalf("releasing a workspace in use = %v, want %v", err, ErrWorkspaceInUse)
	}
	if err := r.Settle(ctx, second.Operation, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := ready(); len(got) != 0 {
		t.Fatalf("a workspace with an unreleased invocation is ready: %v", got)
	}
	if err := r.ConfirmRelease(ctx, second.Operation); err != nil {
		t.Fatal(err)
	}
	if got := ready(); !slices.Equal(got, []string{first.Workspace}) {
		t.Fatalf("ready workspaces = %v", got)
	}
	for range 2 {
		if err := r.ConfirmWorkspaceRelease(ctx, in.Backend, first.Workspace); err != nil {
			t.Fatal(err)
		}
	}
	if got := ready(); len(got) != 0 {
		t.Fatalf("a released workspace is still listed: %v", got)
	}
	if a, err := r.Get(ctx, second.Operation); err != nil || !a.WorkspaceReleased {
		t.Fatalf("the invocation's workspace release: %+v %v", a, err)
	}
}

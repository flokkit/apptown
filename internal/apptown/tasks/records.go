// Package tasks owns execution attempts (design/operations/workers.md).
//
// A step attempt that runs a task records it here before any provider effect:
// its operation key, owner and request digest, then the provider's identity
// before the task starts, then what was observed, and finally that the owner
// settled it. A restarted AppTown reads the unsettled attempts and adopts,
// collects or stops each one instead of running it again.
//
// The package has no workflow or provider knowledge. Callers choose which
// transition applies; this owner decides which transitions an attempt's
// recorded state allows, and keeps its identity and the order of the facts.
package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/tasks/internal/taskssql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/execution"
)

// Observed says how far an attempt got, as last recorded.
type Observed string

const (
	Reserved   Observed = "reserved"   // recorded; the provider may or may not have created it
	Started    Observed = "started"    // the provider started it
	Collecting Observed = "collecting" // it exited; its result is being collected
	Settled    Observed = "settled"    // the owner recorded its outcome
	Unknown    Observed = "unknown"    // its outcome cannot be established
	// Refused: the backend refused it before creating anything, so nothing
	// exists to observe or stop. What remains is recording the step's
	// failure and settling; a restarted AppTown finishes that without asking
	// the backend, whichever backend it then has.
	Refused Observed = "refused"
	// Held: its result was not kept, so its task and workspace on the
	// backend hold the only copy of its work. AppTown never settles or
	// releases it; a person inspecting the backend ends it.
	Held Observed = "held"
)

var (
	// ErrConflict: the same operation key was reserved with a different
	// request. A key names one request, never a changed one.
	ErrConflict = errors.New("this operation was already admitted with a different request")
	// ErrSettled: the attempt is already settled; nothing more is recorded.
	ErrSettled = errors.New("this task attempt is already settled")
	// ErrHeld: the attempt holds the only copy of its work; AppTown
	// records nothing more that would settle or release it.
	ErrHeld = errors.New("this task attempt holds work that was not kept")
	// ErrNotAccepted: the transition does not follow the attempt's
	// recorded state.
	ErrNotAccepted = errors.New("this task attempt's state does not accept that transition")
	// ErrInvocationTaken: another operation already holds this invocation of
	// the step attempt. One invocation of an attempt is one task.
	ErrInvocationTaken = errors.New("this invocation of the step attempt was already reserved under another operation")
)

// ScriptInvocation is the one invocation a script step's attempt runs.
const ScriptInvocation = "script"

// ReleaseBuildInvocation is the one invocation a deploy step's attempt
// runs to build its release on the worker. It is never continued across a
// AppTown restart: its step ends in error, and its task is stopped and
// released (UnsettledStepsIn).
const ReleaseBuildInvocation = "release-build"

type Owner struct {
	ProjectID ids.ID
	RunID     ids.ID
	StepID    ids.ID
	Attempt   int64
}

type Admission struct {
	Operation string
	Owner     Owner
	// Invocation names this run of a program within the step attempt
	// (ScriptInvocation for a script). An attempt has at most one task per
	// invocation.
	Invocation string
	// Workspace names the backend workspace the task shares with its
	// attempt's other invocations; empty when the task owns its own.
	Workspace string
	Backend   string
	Container string // the provider name derived from Operation
	Digest    string // the canonical request; the same key must carry the same digest
	// Source is the commit a remote task was given, which its result must
	// build on. Empty for a task that mounts AppTown's workspace.
	Source string
	// Admitted is the admission the task was given: profile, limits,
	// network and expiry. Deadline is when its wall-clock budget ends. A
	// recovered task keeps both; neither is renewed by a restart.
	Admitted execution.Admission
	Deadline time.Time
}

// same reports whether a reservation names the same request. Times are
// compared as instants: the database does not keep their location.
func (a Admission) same(b Admission) bool {
	return a.Operation == b.Operation && a.Owner == b.Owner && a.Invocation == b.Invocation && a.Workspace == b.Workspace && a.Backend == b.Backend && a.Container == b.Container && a.Digest == b.Digest &&
		a.Source == b.Source && a.Deadline.Equal(b.Deadline) && a.Admitted.Profile == b.Admitted.Profile && a.Admitted.Limits == b.Admitted.Limits &&
		a.Admitted.Network == b.Admitted.Network && a.Admitted.Expires.Equal(b.Admitted.Expires)
}

type Attempt struct {
	Admission
	ProviderID string
	StopWanted bool
	Observed   Observed
	ExitCode   *int
	// TimedOut: the decided outcome is a timeout (ExitCode -1). Kept once
	// recorded, through settlement.
	TimedOut bool
	// Detail explains the last transition to people. Nothing decides by it.
	Detail string
	// Released: the backend confirmed the settled task released, or nothing
	// of it was ever held there. Only a settled attempt is released.
	Released bool
	// WorkspaceReleased: the backend confirmed the shared workspace the
	// attempt names released (ConfirmWorkspaceRelease).
	WorkspaceReleased bool
}

type Records struct {
	db *database.DB
	// Now is AppTown's clock, which stamps an attempt's reservation,
	// stop request and releases; nil is the real one.
	Now func() time.Time
}

func New(db *database.DB) *Records { return &Records{db: db} }

func (r *Records) now() time.Time { return clock.Or(r.Now)() }

// Reserve records an attempt before any provider effect. Reserving the same
// operation again returns the existing attempt, so a retry after a lost reply
// finds what was already admitted; a different request under the same key is
// a conflict.
func (r *Records) Reserve(ctx context.Context, in Admission) (Attempt, error) {
	if in.Operation == "" || in.Invocation == "" || in.Backend == "" || in.Container == "" || in.Digest == "" || in.Owner.StepID.IsZero() || in.Owner.Attempt <= 0 ||
		in.Admitted.Profile == "" || in.Deadline.IsZero() {
		return Attempt{}, fmt.Errorf("a task attempt needs its operation, invocation, owner, backend, container, request digest, admission and deadline")
	}
	// The database keeps microseconds: the deadline is stored, and compared,
	// at that precision.
	in.Deadline = in.Deadline.UTC().Truncate(time.Microsecond)
	admitted, err := json.Marshal(in.Admitted)
	if err != nil {
		return Attempt{}, err
	}
	row, err := taskssql.New(r.db.SQL()).ReserveAttempt(ctx, taskssql.ReserveAttemptParams{Operation: in.Operation, ProjectID: in.Owner.ProjectID, RunID: in.Owner.RunID,
		StepID: in.Owner.StepID, Attempt: in.Owner.Attempt, Invocation: in.Invocation, Workspace: in.Workspace, Backend: in.Backend, Container: in.Container, Digest: in.Digest, Source: in.Source,
		Admission: string(admitted), Deadline: sql.NullTime{Time: in.Deadline, Valid: true}, At: r.now()})
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "23505" {
		return Attempt{}, ErrInvocationTaken
	}
	if err != nil {
		return Attempt{}, err
	}
	a, err := attemptOf(row)
	if err != nil {
		return Attempt{}, err
	}
	if !a.Admission.same(in) {
		return a, ErrConflict
	}
	return a, nil
}

// Get returns an attempt by its operation key.
func (r *Records) Get(ctx context.Context, operation string) (Attempt, error) {
	row, err := taskssql.New(r.db.SQL()).AttemptByOperation(ctx, operation)
	if err != nil {
		return Attempt{}, err
	}
	return attemptOf(row)
}

// RecordProvider records the provider's identity for the attempt, before the
// task is started. A different identity for the same attempt is refused.
func (r *Records) RecordProvider(ctx context.Context, operation, providerID string) error {
	n, err := taskssql.New(r.db.SQL()).RecordProvider(ctx, taskssql.RecordProviderParams{Operation: operation, ProviderID: providerID, At: r.now()})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("task %s already has another provider identity or is settled", operation)
	}
	return nil
}

// The transitions below are the attempt's lifecycle. Each accepts only its
// legal prior states: settled is final; held is final for AppTown (a
// person inspecting the backend ends it); refused only settles with nothing
// held (SettleNothingHeld). A recorded exit code and timeout are never
// erased: a transition given no code keeps the recorded one, and nothing
// clears TimedOut. A transition from a state it does not accept returns
// ErrSettled, ErrHeld or ErrNotAccepted.

// Start records that the provider started the attempt's task.
func (r *Records) Start(ctx context.Context, operation string) error {
	return r.transition(ctx, operation, "a start", func(q *taskssql.Queries, at time.Time) (int64, error) {
		return q.StartAttempt(ctx, taskssql.StartAttemptParams{Operation: operation, At: at})
	})
}

// Exited records the task's exit with code: its outcome is decided and
// being collected. A recorded timeout is never turned into an exit.
func (r *Records) Exited(ctx context.Context, operation string, code int) error {
	return r.transition(ctx, operation, "an exit", func(q *taskssql.Queries, at time.Time) (int64, error) {
		return q.RecordExit(ctx, taskssql.RecordExitParams{Operation: operation, ExitCode: sql.NullInt32{Int32: int32(code), Valid: true}, At: at})
	})
}

// TimedOut records that the task's budget ended: its outcome is decided as
// a timeout (exit code -1) and being collected, and its stop is wanted, in
// one write.
func (r *Records) TimedOut(ctx context.Context, operation string) error {
	return r.transition(ctx, operation, "a timeout", func(q *taskssql.Queries, at time.Time) (int64, error) {
		return q.RecordTimeout(ctx, taskssql.RecordTimeoutParams{Operation: operation, At: at})
	})
}

// Refuse records that the backend refused the task before creating
// anything. What remains is failing the step and SettleNothingHeld.
func (r *Records) Refuse(ctx context.Context, operation, reason string) error {
	return r.transition(ctx, operation, "a refusal", func(q *taskssql.Queries, at time.Time) (int64, error) {
		return q.RecordRefusal(ctx, taskssql.RecordRefusalParams{Operation: operation, Detail: reason, At: at})
	})
}

// RecordUnknown records that the attempt's outcome cannot be established.
// Once its step has moved on, its task is stopped, settled and released.
func (r *Records) RecordUnknown(ctx context.Context, operation, reason string) error {
	return r.transition(ctx, operation, "an unknown outcome", func(q *taskssql.Queries, at time.Time) (int64, error) {
		return q.RecordUnknown(ctx, taskssql.RecordUnknownParams{Operation: operation, Detail: reason, At: at})
	})
}

// Hold records that the attempt's result was not kept: its task and
// workspace on the backend hold the only copy of its work. A held attempt
// is never settled or released by AppTown.
func (r *Records) Hold(ctx context.Context, operation, reason string) error {
	return r.transition(ctx, operation, "a hold", func(q *taskssql.Queries, at time.Time) (int64, error) {
		return q.HoldAttempt(ctx, taskssql.HoldAttemptParams{Operation: operation, Detail: reason, At: at})
	})
}

// Settle records that the owner recorded the attempt's outcome; its task
// is then released. A nil code keeps the recorded one.
func (r *Records) Settle(ctx context.Context, operation string, code *int, why string) error {
	exit := sql.NullInt32{}
	if code != nil {
		exit = sql.NullInt32{Int32: int32(*code), Valid: true}
	}
	return r.transition(ctx, operation, "a settlement", func(q *taskssql.Queries, at time.Time) (int64, error) {
		return q.SettleAttempt(ctx, taskssql.SettleAttemptParams{Operation: operation, ExitCode: exit, Detail: why, At: at})
	})
}

// transition runs one guarded write and, when it changes nothing, says
// which prior state refused it.
func (r *Records) transition(ctx context.Context, operation, what string, write func(*taskssql.Queries, time.Time) (int64, error)) error {
	n, err := write(taskssql.New(r.db.SQL()), r.now())
	if err != nil || n == 1 {
		return err
	}
	return r.refusal(ctx, operation, what)
}

// refusal names why a transition of operation changed nothing.
func (r *Records) refusal(ctx context.Context, operation, what string) error {
	a, err := r.Get(ctx, operation)
	switch {
	case err != nil:
		return err
	case a.Observed == Settled:
		return ErrSettled
	case a.Observed == Held:
		return ErrHeld
	}
	return fmt.Errorf("%w: task %s is %s, and %s is not recorded from there", ErrNotAccepted, operation, a.Observed, what)
}

// RequestStop records that the attempt must stop. It is durable: a restarted
// AppTown carries it out.
func (r *Records) RequestStop(ctx context.Context, operation string) error {
	n, err := taskssql.New(r.db.SQL()).RequestStop(ctx, taskssql.RequestStopParams{Operation: operation, At: r.now()})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSettled
	}
	return nil
}

// SettleNothingHeld settles an attempt of which nothing is held on its
// backend (it was refused before anything was created, or another request's
// task holds its key), and records it released in the same write: nothing is
// ever released under its key, which may name another's task. A held
// attempt is not settled this way, or any other.
func (r *Records) SettleNothingHeld(ctx context.Context, operation, detail string) error {
	return r.transition(ctx, operation, "a settlement with nothing held", func(q *taskssql.Queries, at time.Time) (int64, error) {
		return q.SettleNothingHeld(ctx, taskssql.SettleNothingHeldParams{Operation: operation, Detail: detail, At: sql.NullTime{Time: at, Valid: true}})
	})
}

// ConfirmRelease records that the backend confirmed a settled attempt's task
// released. Only a settled attempt is released; confirming again changes
// nothing.
func (r *Records) ConfirmRelease(ctx context.Context, operation string) error {
	n, err := taskssql.New(r.db.SQL()).ConfirmRelease(ctx, taskssql.ConfirmReleaseParams{Operation: operation, At: sql.NullTime{Time: r.now(), Valid: true}})
	if err != nil || n == 1 {
		return err
	}
	a, err := r.Get(ctx, operation)
	switch {
	case err != nil:
		return err
	case a.Observed != Settled:
		return fmt.Errorf("task %s is %s; only a settled task is released", operation, a.Observed)
	}
	return nil
}

// Unreleased lists up to max settled attempts on backend whose release it has
// not confirmed, oldest first.
func (r *Records) Unreleased(ctx context.Context, backend string, max int) ([]Attempt, error) {
	rows, err := taskssql.New(r.db.SQL()).UnreleasedAttempts(ctx, taskssql.UnreleasedAttemptsParams{Backend: backend, MaxRows: int32(max)})
	if err != nil {
		return nil, err
	}
	return attemptsOf(rows)
}

// Invocations lists a step attempt's invocations, in the order they were
// reserved.
func (r *Records) Invocations(ctx context.Context, stepID ids.ID, attempt int64) ([]Attempt, error) {
	rows, err := taskssql.New(r.db.SQL()).AttemptInvocations(ctx, taskssql.AttemptInvocationsParams{StepID: stepID, Attempt: attempt})
	if err != nil {
		return nil, err
	}
	return attemptsOf(rows)
}

// UnreleasedWorkspaces lists up to max shared workspaces on backend whose
// every task is settled and released, and whose own release the backend
// has not confirmed.
func (r *Records) UnreleasedWorkspaces(ctx context.Context, backend string, max int) ([]string, error) {
	return taskssql.New(r.db.SQL()).UnreleasedWorkspaces(ctx, taskssql.UnreleasedWorkspacesParams{Backend: backend, MaxRows: int32(max)})
}

// ErrWorkspaceInUse: a task naming the workspace is not settled and
// released, so the workspace's release cannot be recorded.
var ErrWorkspaceInUse = errors.New("a task in this workspace is not settled and released")

// ConfirmWorkspaceRelease records that the backend confirmed a shared
// workspace released. It is refused (ErrWorkspaceInUse) while any task
// naming it is not settled and released; confirming again changes nothing.
func (r *Records) ConfirmWorkspaceRelease(ctx context.Context, backend, workspace string) error {
	if workspace == "" {
		return errors.New("a shared workspace needs its name")
	}
	n, err := taskssql.New(r.db.SQL()).ConfirmWorkspaceRelease(ctx, taskssql.ConfirmWorkspaceReleaseParams{At: sql.NullTime{Time: r.now(), Valid: true}, Workspace: workspace, Backend: backend})
	if err != nil || n > 0 {
		return err
	}
	held, err := taskssql.New(r.db.SQL()).WorkspaceHeld(ctx, taskssql.WorkspaceHeldParams{Workspace: workspace, Backend: backend})
	if err != nil {
		return err
	}
	if held {
		return ErrWorkspaceInUse
	}
	return nil // released before, or no task ever named it
}

// Unsettled lists attempts a restarted AppTown must observe.
func (r *Records) Unsettled(ctx context.Context) ([]Attempt, error) { return UnsettledIn(ctx, r.db) }

// UnsettledIn lists every attempt not yet settled.
func UnsettledIn(ctx context.Context, q database.Queryer) ([]Attempt, error) {
	rows, err := taskssql.New(q.SQL()).UnsettledAttempts(ctx)
	if err != nil {
		return nil, err
	}
	return attemptsOf(rows)
}

func attemptsOf(rows []taskssql.TaskAttempt) ([]Attempt, error) {
	out := make([]Attempt, len(rows))
	for i, row := range rows {
		var err error
		if out[i], err = attemptOf(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UnsettledStepsIn lists the steps whose attempt has an unsettled task, for
// the database-only startup repair to preserve. A release build's step is
// not among them: a restart ends it, and the task, no longer its step's, is
// stopped, settled and released after startup.
func UnsettledStepsIn(ctx context.Context, q database.Queryer) ([]ids.ID, error) {
	return taskssql.New(q.SQL()).UnsettledSteps(ctx, ReleaseBuildInvocation)
}

// UnsettledRunsIn lists the runs with an unsettled task: something they
// started may still be running, so they have not ended.
func UnsettledRunsIn(ctx context.Context, q database.Queryer) ([]ids.ID, error) {
	return taskssql.New(q.SQL()).UnsettledRuns(ctx)
}

// attemptOf reads a stored attempt. An attempt recorded before admissions
// were kept has none, and no deadline: a caller treats it as unbounded only
// where it says so.
func attemptOf(row taskssql.TaskAttempt) (Attempt, error) {
	a := Attempt{Admission: Admission{Operation: row.Operation, Owner: Owner{row.ProjectID, row.RunID, row.StepID, row.Attempt}, Invocation: row.Invocation, Workspace: row.Workspace, Backend: row.Backend, Container: row.Container, Digest: row.Digest, Source: row.Source},
		ProviderID: row.ProviderID, StopWanted: row.Desired == "stop", Observed: Observed(row.Observed), TimedOut: row.TimedOut, Detail: row.Detail, Released: row.ReleasedAt.Valid, WorkspaceReleased: row.WorkspaceReleasedAt.Valid}
	if row.ExitCode.Valid {
		code := int(row.ExitCode.Int32)
		a.ExitCode = &code
	}
	if row.Deadline.Valid {
		a.Deadline = row.Deadline.Time
	}
	if row.Admission != "" {
		if err := json.Unmarshal([]byte(row.Admission), &a.Admitted); err != nil {
			return a, fmt.Errorf("task %s has an unreadable admission: %w", row.Operation, err)
		}
	}
	return a, nil
}

// RelocationAttemptsIn inventories every attempt that may still hold provider
// resources. No display limit or current-backend filter can hide an older owner.
func RelocationAttemptsIn(ctx context.Context, q database.Queryer) ([]Attempt, error) {
	rows, err := taskssql.New(q.SQL()).UnreleasedLayoutAttempts(ctx)
	if err != nil {
		return nil, err
	}
	return attemptsOf(rows)
}

package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/operations/internal/operationssql"
)

// StepLog is what a running operation writes as it goes: its steps, the
// facts it records before it makes something, and the checks it asks for.
// A step that cannot be written does not stop the work; the end says so.
type StepLog struct {
	s    *Service
	op   *Operation
	kind Kind
	// lost is the first error writing a step: the steps shown may be behind.
	lost error
}

// Operation is the running operation as the log has it.
func (l *StepLog) Operation() Operation { return *l.op }

func (l *StepLog) write(ctx context.Context) {
	ctx = context.WithoutCancel(ctx) // a step happened: its log is written though ctx has ended
	steps, _ := json.Marshal(l.op.Steps)
	if _, err := operationssql.New(l.s.db.SQL()).RecordOperationSteps(ctx, operationssql.RecordOperationStepsParams{ID: l.op.ID, Steps: steps, At: l.s.now()}); err != nil && l.lost == nil {
		l.lost = err
	}
}

// Begin starts a step, ending any step still open as done.
func (l *StepLog) Begin(ctx context.Context, name string) {
	l.End(ctx, "", 0)
	l.op.Steps = append(l.op.Steps, OperationStep{Name: name, Started: l.s.now()})
	l.write(ctx)
}

// End ends the open step as done, with what it found and moved.
func (l *StepLog) End(ctx context.Context, detail string, bytes int64) {
	if cur := l.op.Current(); cur != nil {
		now := l.s.now()
		cur.Ended, cur.Detail, cur.Bytes = &now, detail, bytes
		l.write(ctx)
	}
}

// Fail ends the open step as failed with why.
func (l *StepLog) Fail(ctx context.Context, why string) {
	if cur := l.op.Current(); cur != nil {
		now := l.s.now()
		cur.Ended, cur.Failed, cur.Detail = &now, true, why
		l.write(ctx)
	}
}

// Unknown ends the open step as not known: the forge asked for an effect
// and got no answer it can trust (a timeout, a lost response), so whether
// it happened is not known.
func (l *StepLog) Unknown(ctx context.Context, why string) {
	if cur := l.op.Current(); cur != nil {
		now := l.s.now()
		cur.Ended, cur.Unknown, cur.Detail = &now, true, why
		l.write(ctx)
	}
}

// Record writes a fact into the operation's result while it runs, before
// the thing it names is made, so whoever looks after a restart finds it.
// It fails when the operation is no longer running.
func (l *StepLog) Record(ctx context.Context, key string, value any) error {
	ctx = context.WithoutCancel(ctx) // a result found: recorded though ctx has ended
	if l.op.Result == nil {
		l.op.Result = map[string]any{}
	}
	l.op.Result[key] = value
	res, err := json.Marshal(l.op.Result)
	if err != nil {
		return err
	}
	n, err := operationssql.New(l.s.db.SQL()).RecordOperationResult(ctx, operationssql.RecordOperationResultParams{ID: l.op.ID, Result: res, At: l.s.now()})
	if err == nil && n != 1 {
		err = fmt.Errorf("operation %d is no longer running", l.op.ID)
	}
	return err
}

// CancelRequested says whether a person asked the operation to stop; asked
// between safe steps only.
func (l *StepLog) CancelRequested(ctx context.Context) bool {
	o, err := l.s.ByID(ctx, l.op.ID)
	return err == nil && o.CancelRequested
}

// ErrAuthorityWithdrawn is a captured credential, or its grants, no longer
// allowing what the operation does next.
var ErrAuthorityWithdrawn = errors.New("authority withdrawn")

// StillAllowed asks whether the authority captured at admission still
// allows the kind's capability in the operation's project: its credential
// is still live (not revoked, expired, logged out or withdrawn), its
// current bounds are within the captured ones, and current grants allow
// it. An owner calls it before each consequential effect; an error wraps
// ErrAuthorityWithdrawn with why, and the owner ends the operation failed
// without the effect.
func (l *StepLog) StillAllowed(ctx context.Context) error {
	if l.s.still == nil {
		return fmt.Errorf("%w: authority cannot be checked", ErrAuthorityWithdrawn)
	}
	err := l.s.still.RequireStill(ctx, l.op.Authority.Principal(), access.Permission{Capability: l.kind.Capability,
		Resource: projectResource(l.op.ProjectID), Context: access.Context{Route: l.op.Kind, Consequential: true}})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorityWithdrawn, err)
	}
	return nil
}

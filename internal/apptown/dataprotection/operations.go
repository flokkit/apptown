package dataprotection

import (
	"context"
	"errors"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/fault"
)

// Data operations (design/subsystems/deployment.md, "Protecting data
// off-host") run through the operations owner (internal/apptown/operations): a copy, a
// drill, a take-down, a rehearsal or a restored copy can take half an
// hour, so each is a row in the operations ledger, admitted here with its
// River job and run once by the operations worker. This file registers
// deployment's kinds and admits them; the kinds' bodies are in
// data_operations.go, rehearsal.go, restored_copy.go and
// restore_operation.go.

// Deployment's operation kinds.
const (
	OperationCopy     = "deployment.copy"
	OperationDrill    = "deployment.drill"
	OperationTakeDown = "deployment.take-down"
	// OperationRehearsal is a restore rehearsal.
	OperationRehearsal = "deployment.rehearsal"
	// OperationRestore starts a restored copy; OperationRestoredCopyEnd
	// takes one down.
	OperationRestore         = "deployment.restored-copy"
	OperationRestoredCopyEnd = "deployment.restored-copy-end"
)

// runEnd is how a kind's body says it ended; outcomeOf maps it to the
// ledger's state and verification.
type runEnd uint8

const (
	// OperationVerified: done, and what it made was shown to come back.
	OperationVerified runEnd = iota + 1
	// OperationNotVerified: done, and nothing checked that it comes back.
	OperationNotVerified
	// OperationDone: done; verification does not apply.
	OperationDone
	// OperationFailed: it stopped; its detail names what is left and where.
	OperationFailed
)

// A copy or a drill that has started is not stopped part way.
var (
	ErrCopyNotStopped  = fault.New(fault.Conflict, "a copy that has started runs to its end; it can be cancelled only while it waits to start")
	ErrDrillNotStopped = fault.New(fault.Conflict, "a check that has started runs to its end and removes what it made; it can be cancelled only while it waits to start")
)

// registerKinds registers deployment's operation kinds with the ledger.
func (s *Service) registerKinds() {
	if s.ops == nil {
		return
	}
	body := func(run func(context.Context, *tracker) (runEnd, string, map[string]any)) func(context.Context, operations.Operation, *operations.StepLog) operations.Outcome {
		return func(ctx context.Context, op operations.Operation, log *operations.StepLog) operations.Outcome {
			t := newTracker(op, log)
			end, detail, result := run(ctx, t)
			return outcomeOf(end, detail, result, t)
		}
	}
	for _, k := range []operations.Kind{
		// A copy and a drill run to their end once started: each is one
		// item at a time, and stopping one part way leaves nothing useful.
		// A queued one is cancelled like any other.
		{Name: OperationCopy, Capability: access.CapDeploymentDeploy, CancelRefusal: ErrCopyNotStopped, Execute: body(s.runCopy)},
		{Name: OperationDrill, Capability: access.CapDeploymentRestore, CancelRefusal: ErrDrillNotStopped, Execute: body(s.runDrill)},
		// A take-down stops as asked at the boundaries between its phases:
		// before the app is deleted, before the final copy, before the
		// copy is checked and before each cluster is deleted.
		{Name: OperationTakeDown, Capability: access.CapDeploymentTeardown, Cancellable: true, Execute: body(func(ctx context.Context, t *tracker) (runEnd, string, map[string]any) {
			// A restored copy is taken down by its own rule: no final copy.
			if dep, err := s.deployments.ByName(ctx, t.op.ProjectID, t.op.Deployment); err == nil && dep.Kind == deployment.Restored {
				return s.runRestoredTakeDown(ctx, t)
			}
			withProject, _ := t.op.Input["withProject"].(bool)
			// Any other target's take-down is a removal, its final backup
			// on the host first, each effect asking again.
			if removal, _ := t.op.Input["removal"].(bool); removal {
				return s.runRemoval(ctx, t, withProject)
			}
			return s.runTakeDown(ctx, t, withProject)
		})},
		// A rehearsal and a restored copy are not stopped part way: each
		// takes itself down when it ends.
		{Name: OperationRehearsal, Capability: access.CapDeploymentRestore, CancelRefusal: ErrRehearsalNotStopped, Execute: body(s.runRehearsal)},
		{Name: OperationRestore, Capability: access.CapDeploymentRestore, CancelRefusal: ErrRestoreNotStopped, Execute: body(s.runRestore)},
		{Name: OperationRestoredCopyEnd, Capability: access.CapDeploymentRestore, CancelRefusal: ErrRestoredCopyEndNotStopped, Execute: body(s.runRestoredTakeDown)},
		// A restore of a set over a deployment runs to its end once started;
		// it needs the deploy grant, as its door always has. Its receipt is
		// written with its end, and an AppTown's stop leaves what it may have
		// stopped named, never started on a guess (restore_operation.go).
		{Name: OperationRestoreSet, Capability: access.CapDeploymentDeploy, CancelRefusal: ErrRestoreSetNotStopped, Execute: body(s.runRestoreSet),
			SettleIn: s.settleRestore, Interrupted: restoreInterrupted},
	} {
		k.Read = access.CapDeploymentRead
		s.ops.Register(k)
	}
}

// outcomeOf is how the ledger records a kind's end. What the tracker saw
// on the way overrides a plain failure: an effect with no trustworthy
// answer is unknown, a stop asked for is cancelled, and withdrawn authority
// says so. A success that left something behind (a drill database that was
// not removed) is not a success: it fails (or is unknown, when whether it
// was removed is not known), keeping its verification as a fact.
func outcomeOf(end runEnd, detail string, result map[string]any, t *tracker) operations.Outcome {
	if t.note != "" && end != OperationFailed {
		detail = strings.TrimSpace(detail + " " + t.note)
	}
	out := operations.Outcome{Detail: detail, Result: result}
	switch end {
	case OperationVerified:
		out.State, out.Verification = operations.Succeeded, operations.Verified
	case OperationNotVerified:
		out.State, out.Verification = operations.Succeeded, operations.NotVerified
	case OperationDone:
		out.State = operations.Succeeded
	case OperationFailed:
		out.State = operations.Failed
	default:
		out.State = operations.Unknown
	}
	if out.State == operations.Succeeded && len(t.left) > 0 {
		out.State = operations.Failed
		out.Detail = strings.TrimSpace(out.Detail + " It left " + strings.Join(t.left, "; ") + " for a person to remove.")
	}
	if out.State == operations.Failed {
		switch {
		case t.indeterminate:
			out.State = operations.Unknown
		case t.stopped:
			out.State = operations.Cancelled
		case t.withdrawn != nil && !strings.HasPrefix(out.Detail, "authority withdrawn"):
			out.Detail = "authority withdrawn: " + out.Detail
		}
	}
	return out
}

// tracker is a running operation as deployment's kinds read and write it.
type tracker struct {
	log *operations.StepLog
	op  opFacts
	// note is said at the end of an operation that did not fail (older
	// copies that were not pruned).
	note string
	// withdrawn is the refusal of a check before an effect, when one ended it.
	withdrawn error
	// stopped says the kind stopped at a safe boundary because a person
	// asked it to (stop).
	stopped bool
	// indeterminate says a provider effect got no answer AppTown can
	// trust (providerFailed): whether it happened is not known.
	indeterminate bool
	// left names what the operation made and could not remove.
	left []string
}

// opFacts are what the kinds read of their operation.
type opFacts struct {
	ID         ids.ID
	ProjectID  ids.ID
	Deployment string
	// DeploymentID is the deployment the operation was admitted on.
	DeploymentID ids.ID
	AskedBy      string
	// Authority is who admitted it, with the credential its checks ask
	// again: what a receipt of its effects is attributed to.
	Authority access.Principal
	Input     map[string]any
}

func newTracker(op operations.Operation, log *operations.StepLog) *tracker {
	name, _ := op.Input["deployment"].(string)
	id := inputID(op.Input, "deploymentId") // an input with no deployment names none
	return &tracker{log: log, op: opFacts{ID: op.ID, ProjectID: op.ProjectID, Deployment: name, DeploymentID: id, AskedBy: op.AskedBy, Authority: op.Authority.Principal(), Input: op.Input}}
}

func (t *tracker) begin(ctx context.Context, name string)              { t.log.Begin(ctx, name) }
func (t *tracker) end(ctx context.Context, detail string, bytes int64) { t.log.End(ctx, detail, bytes) }
func (t *tracker) fail(ctx context.Context, why string)                { t.log.Fail(ctx, why) }
func (t *tracker) record(ctx context.Context, key string, v any) error {
	return t.log.Record(ctx, key, v)
}
func (t *tracker) cancelled(ctx context.Context) bool { return t.log.CancelRequested(ctx) }

// facts are what the operation has recorded so far (record).
func (t *tracker) facts() map[string]any { return t.log.Operation().Result }

// stop says whether a person asked the operation to stop, and records that
// it stops here. A kind asks it only at a safe boundary between steps.
func (t *tracker) stop(ctx context.Context) bool {
	if t.cancelled(ctx) {
		t.stopped = true
		return true
	}
	return false
}

// providerFailed ends the open step after a provider call for an effect
// failed. When the provider refused (deploy.Refused) the step failed and
// the effect did not happen; otherwise (a timeout, a lost answer, a
// server error) whether it happened is not known, the step says so and
// the operation will end unknown. It reports whether the outcome is
// unknown.
func (t *tracker) providerFailed(ctx context.Context, err error) bool {
	if deploy.Refused(err) {
		t.fail(ctx, err.Error())
		return false
	}
	t.indeterminate = true
	t.log.Unknown(ctx, "no answer AppTown can trust, so whether it happened is not known: "+err.Error())
	return true
}

// allowed asks whether the authority that admitted the operation still
// allows what it does next. A kind calls it before each consequential
// effect and, refused, ends failed without the effect.
func (t *tracker) allowed(ctx context.Context) error {
	if err := t.log.StillAllowed(ctx); err != nil {
		t.withdrawn = err
		return err
	}
	return nil
}

// admit records an operation on a deployment and queues it, in one
// transaction under the plan's locking order: the project's release lock,
// the admission lock, then eligible (the kind's own reads), then the
// ledger's hold check and row. A hold that meets a live one refuses with
// operations.HeldError.
func (s *Service) admit(ctx context.Context, dep *deployment.Deployment, kind string, actor access.Principal, holds []string, input map[string]any, eligible func(*database.Tx) error) (*operations.Operation, error) {
	if s.ops == nil {
		return nil, errors.New("data operations are not configured")
	}
	if input == nil {
		input = map[string]any{}
	}
	input["deployment"], input["deploymentId"] = dep.Name, dep.ID
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*operations.Operation, error) {
		if err := deployment.LockProjectIn(ctx, tx, dep.ProjectID); err != nil {
			return nil, err
		}
		if err := refuseUnlessAdmittedIn(ctx, tx, dep.ProjectID, kind); err != nil {
			return nil, err
		}
		if err := operations.LockIn(ctx, tx); err != nil {
			return nil, err
		}
		if eligible != nil {
			if err := eligible(tx); err != nil {
				return nil, err
			}
		}
		return s.ops.AdmitIn(ctx, tx, operations.Spec{Kind: kind, ProjectID: dep.ProjectID, Subject: deployment.SubjectOf(dep), Holds: holds, Authority: actor, Input: input})
	})
}

// refuseUnlessAdmittedIn is deployment's admission against the project's
// lifecycle: a take-down passes while the project is being deleted (taking
// its deployments down is what a deletion does); anything else needs an
// active project.
func refuseUnlessAdmittedIn(ctx context.Context, tx *database.Tx, projectID ids.ID, kind string) error {
	if kind == OperationTakeDown || kind == OperationRestoredCopyEnd {
		l, err := project.LifecycleIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if l.State == project.Active || l.State == project.Deleting {
			return nil
		}
		return &project.NotActiveError{ProjectID: projectID, State: l.State}
	}
	return project.RefuseUnlessActiveIn(ctx, tx, projectID)
}

// inputID reads a record id an operation's input or result names: a string
// in JSON. Absent or unreadable is Nil (none).
func inputID(m map[string]any, key string) ids.ID {
	switch v := m[key].(type) {
	case ids.ID: // not yet through JSON
		return v
	case string:
		id, _ := ids.Parse(v)
		return id
	}
	return ids.Nil
}

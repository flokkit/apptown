package projectlifecycle

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/dataprotection"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
)

// Deleting a project (design/subsystems/project-deletion.md):
//
//	active → deleting → deleted (kept) → purged
//
// A person's deletion marks the project deleting, under a scoped system
// binding their decision grants: it covers taking the project's
// deployments down and purging it once its kept period ends, and nothing
// else. The deletion pass takes the deployments down one at a time, main
// last; when none is left the project is deleted and kept, read-only, with
// its backups, until kept_until. Restore project returns it to active
// until the first purge is admitted. The purge (an operation in the
// ledger) removes its backup sets, its hub repository and its records,
// and archives its GitLab copy.

// CleanupQueue queues a deleted project's cleanup inside the caller's
// transaction: the job commits with the purge or a retry, or not at all.
type CleanupQueue interface {
	EnqueueCleanupIn(ctx context.Context, tx *database.Tx, projectID ids.ID, round int) error
	// LiveIn says whether the round's job is still queued or running.
	LiveIn(ctx context.Context, tx *database.Tx, projectID ids.ID, round int) (bool, error)
	// EnqueuePassIn queues the deletion pass for one project, to run after.
	EnqueuePassIn(ctx context.Context, tx *database.Tx, projectID ids.ID, after time.Time) error
}

// ErrCleanupStillRunning refuses a retry while the round's job is still live.
var ErrCleanupStillRunning = fault.New(fault.Conflict, "that project's cleanup is still being tried; wait for it to finish or give up")

// CleanupPlan is everything a purge commits to remove outside the database
// once the records are gone: the identifiers a cleanup needs are read
// before their rows are deleted. Change and run idList are never reused; the
// slug-named resources are safe because the slug stays held until the
// cleanup is done (project.ErrSlugHeldByCleanup). The purge itself removes
// the hub repository and archives the GitLab copy before the records go, so
// a plan made by a purge names neither.
type CleanupPlan struct {
	Slug            string   `json:"slug"`
	RunIDs          []ids.ID `json:"runIds"`
	ChangeIDs       []ids.ID `json:"changeIds"`
	PreviewSteps    []ids.ID `json:"previewSteps"`              // run-step preview stacks, by step
	Transcripts     []string `json:"transcripts,omitempty"`     // kept agent transcripts, by digest
	GitLabPath      string   `json:"gitlabPath,omitempty"`      // where the GitLab copy was
	GitLabProjectID int64    `json:"gitlabProjectId,omitempty"` // which GitLab project it is, for ever
	GitLabArchive   string   `json:"gitlabArchive,omitempty"`   // the name it is archived under
}

// GitLabProjects names the GitLab project at a path by its permanent id.
type GitLabProjects interface {
	ProjectID(ctx context.Context, pathNS string) (int64, error)
}

// DeletionData is what a deletion asks of data protection: taking one of
// the project's deployments down under the deletion's binding, and
// removing backup sets at purge.
type DeletionData interface {
	TakeDownForDeletion(ctx context.Context, projectID ids.ID, name string, binding access.Principal) (*operations.Operation, error)
	PurgeProjectBackups(ctx context.Context, projectID ids.ID, allowed func(context.Context) error) (int, []string, error)
	PurgeOrphanBackups(ctx context.Context, now time.Time) (int, []string, error)
}

// DeletionRecords are the project's deployments as the deletion reads them.
type DeletionRecords interface {
	ForProject(ctx context.Context, projectID ids.ID) ([]*deployment.Deployment, error)
}

// EvalSources names the live evals with arms working from a project's code.
type EvalSources interface {
	LiveEvalsFromIn(ctx context.Context, tx *database.Tx, projectID ids.ID) ([]ids.ID, error)
}

// DeletionDependencies is what a deletion is built from.
type DeletionDependencies struct {
	Sessions ConsoleAdmission
	Records  DeletionRecords
	Data     DeletionData
	GitLab   GitLabProjects
	// Archive archives the GitLab copy (Cleanup.archiveGitLab).
	Archive    Cleanup
	Queue      CleanupQueue
	Operations *operations.Service
	// Authority checks the person asking: delete, restore, purge now.
	Authority access.Authorizer
	// Evals are live work too: an eval's arms copy from the project.
	Evals EvalSources
	// Now is the clock the grace and kept periods are measured by; nil
	// means time.Now.
	Now func() time.Time
}

type Deletion struct {
	lifecycle *Service
	deps      DeletionDependencies
	// held are the console blocks a deleting or deleted project holds,
	// until it is restored or purged (HoldBlocks re-applies them at start).
	mu   sync.Mutex
	held map[ids.ID]func()
	now  func() time.Time
}

// The lifecycle's timing.
const (
	// DeletionGrace is how long a deletion waits before its first
	// take-down, so it can still be cancelled.
	DeletionGrace = 10 * time.Minute
	// DefaultKeptDays is how long a deleted project is kept before it is
	// purged, unless the instance setting says otherwise.
	DefaultKeptDays = 30
	// KeptDaysSetting is the instance setting that changes it.
	KeptDaysSetting = "project.kept-days"
	// PurgeKind is the purge's operation kind.
	PurgeKind = "project.purge"
	// DeletionPassJob and PurgePassJob are the lifecycle's River passes.
	DeletionPassJob = "project-deletion-pass"
	PurgePassJob    = "project-purge-pass"
)

func NewDeletion(lifecycle *Service, deps DeletionDependencies) *Deletion {
	d := &Deletion{lifecycle: lifecycle, deps: deps, held: map[ids.ID]func(){}, now: clock.Or(deps.Now)}
	if deps.Operations != nil {
		deps.Operations.Register(operations.Kind{Name: PurgeKind, Capability: access.CapProjectDelete, Read: access.CapProjectRead,
			CancelRefusal: ErrPurgeNotStopped, Execute: d.runPurge})
	}
	return d
}

// ErrPurgeNotStopped: a purge removes things that do not come back; it is
// not stopped part way.
var ErrPurgeNotStopped = fault.New(fault.Conflict, "a purge is not stopped part way: what it removed does not come back")

// DeletionConflict is a deletion refused by the project's state.
type DeletionConflict struct{ Err error }

func (e *DeletionConflict) Error() string         { return e.Err.Error() }
func (e *DeletionConflict) FaultKind() fault.Kind { return fault.Conflict }
func (e *DeletionConflict) Unwrap() error         { return e.Err }

// Status is where a project's deletion stands, as its page shows it.
type Status struct {
	project.Lifecycle
	Slug string `json:"slug"`
	// Remaining are the deployments still to be taken down, main last.
	Remaining []string `json:"remaining,omitempty"`
	// TakeDown is the deletion's latest take-down, when there was one.
	TakeDown *operations.Operation `json:"takeDown,omitempty"`
	// Purge is the latest purge, when there was one.
	Purge *operations.Operation `json:"purge,omitempty"`
	// BindingRevoked says the deletion's authority was withdrawn.
	BindingRevoked bool `json:"bindingRevoked,omitempty"`
}

func (d *Deletion) require(ctx context.Context, projectID ids.ID, route string, caps ...access.Capability) (access.Principal, error) {
	if d.deps.Authority == nil {
		return access.Principal{}, access.ErrAuthorityUnavailable
	}
	var pr access.Principal
	for _, c := range caps {
		res := access.Resource{Type: "project", ID: projectID.String(), ProjectID: projectID}
		if c == access.CapPlatformOperate {
			res = access.Resource{Type: "instance"}
		}
		caller, err := d.deps.Authority.Require(ctx, access.Permission{Capability: c, Resource: res, Context: access.Context{Route: route, Consequential: true}})
		if err != nil {
			return access.Principal{}, err
		}
		pr = caller.Principal
	}
	return pr, nil
}

// keptFor is how long a deleted project is kept (the instance setting).
func (d *Deletion) keptFor(ctx context.Context) time.Duration {
	days := DefaultKeptDays
	if v, err := identity.InstanceSettingIn(ctx, d.lifecycle.db, KeptDaysSetting); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			days = n
		}
	}
	return time.Duration(days) * 24 * time.Hour
}

// Request deletes a project: refused while a chat is opening, active or
// making its last save, and while the project has running work. It
// grants the deletion's binding, marks the project deleting and queues
// the pass, all in one transaction, and returns at once. A project
// already being deleted is re-confirmed: a stalled deletion goes on under
// the new decision's binding.
func (d *Deletion) Request(ctx context.Context, projectID ids.ID) (Status, error) {
	actor, err := d.require(ctx, projectID, "project.delete", access.CapProjectDelete)
	if err != nil {
		return Status{}, err
	}
	return d.request(ctx, projectID, actor, false)
}

// RequestCopy deletes an eval's copy project for the evals owner, which
// decided who may delete its eval: its data is synthetic, so it has no
// grace period and no kept period, and goes straight to its purge.
func (d *Deletion) RequestCopy(ctx context.Context, projectID ids.ID, who string) (Status, error) {
	actor := access.Principal{Kind: "user", Name: who, Via: "evals", Credential: "evals:" + who}
	return d.request(ctx, projectID, actor, true)
}

func (d *Deletion) request(ctx context.Context, projectID ids.ID, actor access.Principal, synthetic bool) (Status, error) {
	p, err := project.ByIDIn(ctx, d.lifecycle.db, projectID)
	if err != nil {
		return Status{}, err
	}
	reheld, err := d.hold(projectID)
	if err != nil {
		return Status{}, &DeletionConflict{Err: err}
	}
	kept := d.keptFor(ctx)
	if synthetic {
		kept = 0
	}
	decision := map[string]any{
		"meaning":    "delete this project: take its deployments down (main last, each with its final copy where the forge takes one), keep it read-only with its backups, then purge it",
		"keptDays":   int(kept / (24 * time.Hour)),
		"purge":      "after the kept period the project's backups, hub repository and records are removed and its GitLab copy is archived",
		"deleter":    actor.Name,
		"credential": actor.Credential,
		"synthetic":  synthetic,
	}
	err = d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		l, err := project.LockedLifecycleIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if l.State == project.Active {
			// Running work refuses the deletion; Work reads it under the
			// project lock held here.
			if _, err := workflow.ProjectRemovalIn(ctx, tx, projectID); err != nil {
				return err
			}
			if d.deps.Evals != nil {
				idList, err := d.deps.Evals.LiveEvalsFromIn(ctx, tx, projectID)
				if err != nil {
					return err
				}
				if len(idList) > 0 {
					return &DeletionConflict{Err: fault.New(fault.Conflict, fmt.Sprintf("live evals copy from this project (%s); let them finish, or stop them, first", evalList(idList)))}
				}
			}
		}
		now := d.now()
		binding, err := identity.GrantBindingIn(ctx, tx, identity.BindingDeletion, projectID, actor.Name, decision, now)
		if err != nil {
			return err
		}
		if l.State == project.Deleting && !l.BindingID.IsZero() {
			if err := identity.RevokeBindingIn(ctx, tx, l.BindingID, actor.Name, "replaced by a re-confirmed deletion", now); err != nil {
				return err
			}
		}
		requester, err := identity.ActorIn(ctx, tx, actor.Name)
		if err != nil {
			return err
		}
		if err := project.MarkDeletingIn(ctx, tx, projectID, requester, binding, synthetic, now); err != nil {
			return err
		}
		after := now.Add(DeletionGrace)
		if synthetic || l.State == project.Deleting {
			after = now
		}
		if err := d.deps.Queue.EnqueuePassIn(ctx, tx, projectID, after); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, actor.Name, projectID, "project.deletion-requested", p.Slug, map[string]any{
			"binding": binding, "decision": decision, "reconfirmed": l.State == project.Deleting})
	})
	if err != nil {
		if reheld {
			d.release(projectID)
		}
		if errors.Is(err, workflow.ErrProjectBusy) || errors.Is(err, project.ErrNotActive) {
			return Status{}, &DeletionConflict{Err: err}
		}
		return Status{}, err
	}
	return d.Status(ctx, projectID)
}

func evalList(idList []ids.ID) string {
	out := make([]string, len(idList))
	for i, id := range idList {
		out[i] = fmt.Sprintf("eval %d", id)
	}
	return strings.Join(out, ", ")
}

// hold takes the project's console block, once: no chat on the project is
// opened, identified or active from here until it is restored or purged.
// It says whether this call took it.
func (d *Deletion) hold(projectID ids.ID) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.held[projectID]; ok {
		return false, nil
	}
	unblock, err := d.deps.Sessions.Block("project", projectID.String())
	if err != nil {
		return false, err
	}
	d.held[projectID] = unblock
	return true, nil
}

func (d *Deletion) release(projectID ids.ID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if unblock, ok := d.held[projectID]; ok {
		unblock()
		delete(d.held, projectID)
	}
}

// HoldBlocks re-applies, at start and before serving, the console block of
// every project being deleted or deleted. A block that cannot be taken
// fails the start.
func (d *Deletion) HoldBlocks(ctx context.Context) error {
	list, err := d.lifecycle.projects.NotActive(ctx)
	if err != nil {
		return err
	}
	for _, p := range list {
		if p.State != project.Deleting && p.State != project.Deleted {
			continue
		}
		if _, err := d.hold(p.ProjectID); err != nil {
			return fmt.Errorf("holding the chats of %s, which is %s: %w", p.Slug, p.State, err)
		}
	}
	return nil
}

// Cancel returns a deleting project to active, only before its first
// take-down was admitted; its binding is revoked.
func (d *Deletion) Cancel(ctx context.Context, projectID ids.ID) (Status, error) {
	actor, err := d.require(ctx, projectID, "project.delete", access.CapProjectDelete)
	if err != nil {
		return Status{}, err
	}
	err = d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		l, err := project.CancelDeletionIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		at := d.now()
		if err := identity.RevokeBindingIn(ctx, tx, l.BindingID, actor.Name, "the deletion was cancelled", at); err != nil {
			return err
		}
		return audit.At(at).RecordIn(ctx, tx, actor.Name, projectID, "project.deletion-cancelled", "", map[string]any{"binding": l.BindingID})
	})
	if err != nil {
		return Status{}, err
	}
	d.release(projectID)
	return d.Status(ctx, projectID)
}

// Restore returns a deleted project to active, with no deployments, until
// its first purge was admitted; its binding is revoked. It needs the
// delete capability and deployment:restore.
func (d *Deletion) Restore(ctx context.Context, projectID ids.ID) (Status, error) {
	actor, err := d.require(ctx, projectID, "project.restore", access.CapProjectDelete, access.CapDeploymentRestore)
	if err != nil {
		return Status{}, err
	}
	err = d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		l, err := project.RestoreIn(ctx, tx, projectID)
		if err != nil {
			return err
		}
		at := d.now()
		if err := identity.RevokeBindingIn(ctx, tx, l.BindingID, actor.Name, "the project was restored", at); err != nil {
			return err
		}
		return audit.At(at).RecordIn(ctx, tx, actor.Name, projectID, "project.restored", "", map[string]any{"binding": l.BindingID, "deletedAt": l.DeletedAt})
	})
	if err != nil {
		return Status{}, err
	}
	d.release(projectID)
	return d.Status(ctx, projectID)
}

// RevokeBinding withdraws a deletion's authority: the deletion pass and
// the purge stop at their next effect, and the page says so. Restoring or
// re-confirming the deletion is what moves it on.
func (d *Deletion) RevokeBinding(ctx context.Context, projectID ids.ID, why string) (Status, error) {
	actor, err := d.require(ctx, projectID, "project.delete", access.CapProjectDelete)
	if err != nil {
		return Status{}, err
	}
	l, err := d.lifecycle.projects.Lifecycle(ctx, projectID)
	if err != nil {
		return Status{}, err
	}
	if l.BindingID.IsZero() {
		return Status{}, fault.New(fault.Conflict, "the project's deletion holds no binding")
	}
	if err := d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		return identity.RevokeBindingIn(ctx, tx, l.BindingID, actor.Name, why, d.now())
	}); err != nil {
		return Status{}, err
	}
	return d.Status(ctx, projectID)
}

// Status is where a project's deletion stands.
func (d *Deletion) Status(ctx context.Context, projectID ids.ID) (Status, error) {
	l, err := d.lifecycle.projects.Lifecycle(ctx, projectID)
	if err != nil {
		return Status{}, err
	}
	out := Status{Lifecycle: l}
	if p, err := project.ByIDIn(ctx, d.lifecycle.db, projectID); err == nil {
		out.Slug = p.Slug
	}
	if l.State == project.Active || l.State == project.Importing {
		return out, nil
	}
	if !l.BindingID.IsZero() {
		if b, err := identity.BindingIn(ctx, d.lifecycle.db, l.BindingID); err == nil {
			out.BindingRevoked = b.Revoked
		}
	}
	if l.State == project.Deleting {
		if out.Remaining, err = d.remaining(ctx, projectID); err != nil {
			return out, err
		}
		if out.TakeDown, err = d.latestTakeDown(ctx, projectID, l); err != nil {
			return out, err
		}
	}
	if d.deps.Operations != nil {
		if ops, err := d.deps.Operations.OnSubject(ctx, subjectOf(projectID), 1); err == nil && len(ops) > 0 {
			out.Purge = &ops[0]
		}
	}
	return out, nil
}

func subjectOf(projectID ids.ID) string { return fmt.Sprintf("project:%d", projectID) }

// remaining are the project's deployments still to be taken down, in the
// order the pass takes them: main last.
func (d *Deletion) remaining(ctx context.Context, projectID ids.ID) ([]string, error) {
	placements, err := d.deps.Records.ForProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return takeDownOrder(placements), nil
}

// takeDownOrder is the deployments in the order a deletion takes them down:
// the deployments' own order (ForProject), with main moved last.
func takeDownOrder(placements []*deployment.Deployment) []string {
	var names []string
	main := false
	for _, placed := range placements {
		switch {
		case placed.Kind != deployment.Standing && placed.Kind != deployment.Restored:
		case placed.Name == "main":
			main = true
		default:
			names = append(names, placed.Name)
		}
	}
	if main {
		names = append(names, "main")
	}
	return names
}

// latestTakeDown is the newest take-down operation this deletion's current
// decision admitted: one under its binding. A re-confirmed deletion has a
// new binding, so the attempt that stalled the earlier decision no longer
// stalls it, and stays on the record as it ended.
func (d *Deletion) latestTakeDown(ctx context.Context, projectID ids.ID, l project.Lifecycle) (*operations.Operation, error) {
	if d.deps.Operations == nil {
		return nil, nil
	}
	ops, err := d.deps.Operations.UnderProject(ctx, projectID, 20)
	if err != nil {
		return nil, err
	}
	for i := range ops {
		op := &ops[i]
		if (op.Kind == dataprotection.OperationTakeDown || op.Kind == dataprotection.OperationRestoredCopyEnd) && op.Authority.Credential == identity.BindingCredential(l.BindingID) {
			return op, nil
		}
	}
	return nil, nil
}

// Pass is the deletion pass for every project being deleted, or for one
// (key project/<id>); each project takes one step.
func (d *Deletion) Pass(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	list, err := project.DeletingIn(ctx, d.lifecycle.db)
	if err != nil {
		return nil, err
	}
	var only ids.ID
	if id, ok := strings.CutPrefix(key, "project/"); ok {
		only, _ = ids.Parse(id)
	}
	out := map[string]any{}
	for _, l := range list {
		if !only.IsZero() && l.ProjectID != only {
			continue
		}
		said, err := d.step(ctx, l)
		if err != nil {
			said = "error: " + err.Error()
			log.Printf("project %d deletion: %v", l.ProjectID, err)
		}
		out[l.ProjectID.String()] = said
	}
	return out, nil
}

// step moves one deleting project on: one take-down, or deleted when
// nothing is left. A take-down that is live is waited for; one that failed
// or is unknown, or a binding revoked, stalls the deletion until a person
// re-confirms it.
func (d *Deletion) step(ctx context.Context, l project.Lifecycle) (string, error) {
	if l.Stalled != "" {
		return "stalled: " + l.Stalled, nil
	}
	if !l.Synthetic && d.now().Before(l.RequestedAt.Time.Add(DeletionGrace)) {
		return "waiting out the grace period", nil
	}
	b, err := identity.BindingIn(ctx, d.lifecycle.db, l.BindingID)
	if err != nil {
		return "", err
	}
	if b.Revoked {
		return d.stall(ctx, l.ProjectID, "the deletion's authority (binding "+b.ID.String()+") was revoked by "+b.RevokedBy)
	}
	binding := identity.BindingPrincipal(l.BindingID, l.RequestedBy)
	for {
		if live, err := d.liveUnder(ctx, l.ProjectID); err != nil || live != "" {
			return live, err
		}
		if last, err := d.latestTakeDown(ctx, l.ProjectID, l); err != nil {
			return "", err
		} else if last != nil && last.State != operations.Succeeded {
			return d.stall(ctx, l.ProjectID, fmt.Sprintf("the take-down of %v (operation %d) ended %s: %s", last.Input["deployment"], last.ID, last.State, last.Detail))
		}
		names, err := d.remaining(ctx, l.ProjectID)
		if err != nil {
			return "", err
		}
		if len(names) == 0 {
			return d.markDeleted(ctx, l)
		}
		name := names[0]
		if err := d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
			return project.NoteTakeDownsStartedIn(ctx, tx, l.ProjectID)
		}); err != nil {
			return "", err
		}
		op, err := d.deps.Data.TakeDownForDeletion(ctx, l.ProjectID, name, binding)
		if err != nil {
			return d.stall(ctx, l.ProjectID, fmt.Sprintf("%s could not be taken down: %v", name, err))
		}
		if op != nil {
			return fmt.Sprintf("taking %s down (operation %d)", name, op.ID), nil
		}
		// Removed now: the next one, in this same pass. One still standing
		// after it says it was removed is not tried again blind.
		after, err := d.remaining(ctx, l.ProjectID)
		if err != nil {
			return "", err
		}
		if len(after) > 0 && after[0] == name {
			return d.stall(ctx, l.ProjectID, name+" was reported taken down but still stands")
		}
	}
}

// liveUnder names a live operation under the project, "" when none is.
func (d *Deletion) liveUnder(ctx context.Context, projectID ids.ID) (string, error) {
	var holder *operations.Operation
	err := d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		var err error
		holder, err = operations.ConflictsIn(ctx, tx, subjectOf(projectID))
		return err
	})
	if err != nil || holder == nil {
		return "", err
	}
	return fmt.Sprintf("waiting on %s (operation %d, %s)", holder.Kind, holder.ID, holder.State), nil
}

func (d *Deletion) stall(ctx context.Context, projectID ids.ID, why string) (string, error) {
	err := d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		if err := project.StallDeletionIn(ctx, tx, projectID, why); err != nil {
			return err
		}
		return audit.At(d.now()).RecordIn(ctx, tx, "forge-system", projectID, "project.deletion-stalled", "", map[string]any{"why": why})
	})
	return "stalled: " + why, err
}

// markDeleted moves the project to deleted once nothing is left: no
// deployment and no live work, rechecked under the project lock.
func (d *Deletion) markDeleted(ctx context.Context, l project.Lifecycle) (string, error) {
	now := d.now().UTC()
	kept := d.keptFor(ctx)
	if l.Synthetic {
		kept = 0
	}
	err := d.lifecycle.db.InTx(ctx, func(tx *database.Tx) error {
		current, err := project.LockedLifecycleIn(ctx, tx, l.ProjectID)
		if err != nil {
			return err
		}
		if current.State != project.Deleting {
			return fault.New(fault.Conflict, "the project is no longer being deleted")
		}
		if names, err := d.remaining(ctx, l.ProjectID); err != nil {
			return err
		} else if len(names) > 0 {
			return fault.New(fault.Conflict, "a deployment remains: "+strings.Join(names, ", "))
		}
		if _, err := workflow.ProjectRemovalIn(ctx, tx, l.ProjectID); err != nil {
			return err
		}
		if holder, err := operations.ConflictsIn(ctx, tx, subjectOf(l.ProjectID)); err != nil {
			return err
		} else if holder != nil {
			return fault.New(fault.Conflict, fmt.Sprintf("operation %d is live", holder.ID))
		}
		if err := project.MarkDeletedIn(ctx, tx, l.ProjectID, now, now.Add(kept)); err != nil {
			return err
		}
		return audit.At(now).RecordIn(ctx, tx, "forge-system", l.ProjectID, "project.deleted-kept", "", map[string]any{
			"binding": l.BindingID, "deleter": l.RequestedBy, "keptUntil": now.Add(kept)})
	})
	if err != nil {
		return "", err
	}
	if l.Synthetic {
		// An eval copy's data is synthetic: it goes straight to its purge.
		if _, err := d.admitPurge(ctx, l.ProjectID, identity.BindingPrincipal(l.BindingID, l.RequestedBy), false); err != nil {
			return "deleted; its purge was not admitted: " + err.Error(), nil
		}
	}
	return "deleted, kept until " + now.Add(kept).Format(time.RFC3339), nil
}

// PurgeNow purges a deleted project at once, before its kept period ends,
// under the instance administrator's own authority.
func (d *Deletion) PurgeNow(ctx context.Context, projectID ids.ID) (*operations.Operation, error) {
	actor, err := d.require(ctx, projectID, "project.purge", access.CapPlatformOperate, access.CapProjectDelete)
	if err != nil {
		return nil, err
	}
	return d.admitPurge(ctx, projectID, actor, true)
}

// PurgePass admits a purge of each deleted project whose kept period has
// ended, under its deletion's binding, and removes the backup sets of
// projects deleted before projects were kept, 30 days after each deletion.
func (d *Deletion) PurgePass(ctx context.Context, _ string, _ map[string]any) (map[string]any, error) {
	due, err := project.PurgesDueIn(ctx, d.lifecycle.db, d.now())
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, l := range due {
		op, err := d.admitPurge(ctx, l.ProjectID, identity.BindingPrincipal(l.BindingID, l.RequestedBy), false)
		switch {
		case err != nil:
			out[l.ProjectID.String()] = "not admitted: " + err.Error()
		default:
			out[l.ProjectID.String()] = fmt.Sprintf("operation %d", op.ID)
		}
	}
	removed, left, err := d.deps.Data.PurgeOrphanBackups(ctx, d.now())
	if err != nil {
		return out, err
	}
	out["orphanSetsRemoved"], out["orphanSetsLeft"] = removed, left
	return out, nil
}

// admitPurge records that a purge was admitted (closing Restore for good)
// and admits it, in one transaction under the project lock. One already
// live is not admitted again.
func (d *Deletion) admitPurge(ctx context.Context, projectID ids.ID, authority access.Principal, now bool) (*operations.Operation, error) {
	if d.deps.Operations == nil {
		return nil, errors.New("operations are not configured")
	}
	p, err := project.ByIDIn(ctx, d.lifecycle.db, projectID)
	if err != nil {
		return nil, err
	}
	// The GitLab copy is named by its permanent id before anything is
	// removed, and archived under a name no later project can take.
	input := map[string]any{"slug": p.Slug, "now": now}
	if _, pathNS, ok := scm.Origin(ctx, hubDir(d.lifecycle.dataDir, p.Slug)); ok && pathNS != "" {
		id, err := d.deps.GitLab.ProjectID(ctx, pathNS)
		if err != nil {
			return nil, fmt.Errorf("the GitLab copy at %s could not be looked up, so the purge cannot archive it; try again: %w", pathNS, err)
		}
		input["gitlabPath"], input["gitlabProjectId"], input["gitlabArchive"] = pathNS, id, fmt.Sprintf("%s-deleted-%d", p.Slug, p.ID)
	} else if earlier, err := d.deps.Operations.OnSubject(ctx, subjectOf(projectID), 20); err != nil {
		return nil, err
	} else {
		// An earlier purge removed the hub repository, so its origin is
		// gone: the GitLab copy is the one that purge named.
		for _, op := range earlier {
			if path, _ := op.Input["gitlabPath"].(string); op.Kind == PurgeKind && path != "" {
				input["gitlabPath"], input["gitlabProjectId"], input["gitlabArchive"] = path, op.Input["gitlabProjectId"], op.Input["gitlabArchive"]
				break
			}
		}
	}
	return database.Transact(ctx, d.lifecycle.db, func(tx *database.Tx) (*operations.Operation, error) {
		l, err := project.LockedLifecycleIn(ctx, tx, projectID)
		if err != nil {
			return nil, err
		}
		if l.State != project.Deleted {
			return nil, fault.New(fault.Conflict, fmt.Sprintf("the project is %s, not deleted, so it is not purged", l.State))
		}
		if err := operations.LockIn(ctx, tx); err != nil {
			return nil, err
		}
		if err := project.StartPurgeIn(ctx, tx, projectID, d.now()); err != nil {
			return nil, err
		}
		return d.deps.Operations.AdmitIn(ctx, tx, operations.Spec{Kind: PurgeKind, ProjectID: projectID, Subject: subjectOf(projectID), Authority: authority, Input: input})
	})
}

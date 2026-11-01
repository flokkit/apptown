package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/stamp"
)

// A project's lifecycle (design/subsystems/project-deletion.md):
//
//	active → deleting → deleted (kept until kept_until) → purged
//	active → importing → active
//
// Only an active project takes new work. Every owner door that creates or
// restarts project work holds the project row (FOR UPDATE, or FOR SHARE)
// and asks RefuseUnlessActiveIn before any effect; a state change holds the
// row FOR UPDATE, so a door either commits first (and the change then sees
// its work) or refuses.
const (
	Active    = "active"
	Importing = "importing"
	Deleting  = "deleting"
	Deleted   = "deleted"
)

// Lifecycle is where a project stands, read in one place.
type Lifecycle struct {
	ProjectID   ids.ID `json:"projectId"`
	State       string `json:"state"`
	RequestedBy string `json:"requestedBy,omitempty"`
	// RequestedByPrincipal is the requester's principal; zero when not resolved to a person.
	RequestedByPrincipal ids.ID `json:"requestedByPrincipal,omitzero"`
	// BindingID is the scoped system binding a deletion and its purge run
	// under (identity's system_bindings).
	BindingID        ids.ID     `json:"bindingId,omitzero"`
	Synthetic        bool       `json:"synthetic,omitempty"`
	TakeDownsStarted bool       `json:"takeDownsStarted,omitempty"`
	Stalled          string     `json:"stalled,omitempty"`
	RequestedAt      stamp.Time `json:"requestedAt,omitzero"`
	DeletedAt        stamp.Time `json:"deletedAt,omitzero"`
	KeptUntil        stamp.Time `json:"keptUntil,omitzero"`
	PurgeStartedAt   stamp.Time `json:"purgeStartedAt,omitzero"`
}

func lifecycleRecord(r projectsql.ProjectLifecycle) Lifecycle {
	return Lifecycle{ProjectID: r.ProjectID, State: r.State, RequestedBy: r.RequestedBy, RequestedByPrincipal: r.RequestedByPrincipal, BindingID: r.BindingID, Synthetic: r.Synthetic,
		TakeDownsStarted: r.TakedownsStarted, Stalled: r.Stalled, RequestedAt: stamp.Of(r.RequestedAt),
		DeletedAt: stamp.Time{NullTime: r.DeletedAt}, KeptUntil: stamp.Time{NullTime: r.KeptUntil}, PurgeStartedAt: stamp.Time{NullTime: r.PurgeStartedAt}}
}

// LifecycleIn is a project's lifecycle; active when it has no row.
func LifecycleIn(ctx context.Context, q database.Queryer, id ids.ID) (Lifecycle, error) {
	r, err := queries(q).ProjectLifecycle(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Lifecycle{ProjectID: id, State: Active}, nil
	}
	if err != nil {
		return Lifecycle{}, err
	}
	return lifecycleRecord(r), nil
}

// NotActiveError refuses new work in a project that is being imported,
// being deleted or deleted.
type NotActiveError struct {
	ProjectID ids.ID
	State     string
}

func (e *NotActiveError) Error() string {
	switch e.State {
	case Deleting:
		return "this project is being deleted, so it takes no new work"
	case Deleted:
		return "this project is deleted (kept, read-only, until it is purged), so it takes no new work"
	case Importing:
		return "this project is being imported, so it takes no new work until the import ends"
	}
	return fmt.Sprintf("this project is %s, so it takes no new work", e.State)
}
func (e *NotActiveError) FaultKind() fault.Kind { return fault.Conflict }
func (e *NotActiveError) FaultCode() string     { return "project-not-active" }

// ErrNotActive matches every NotActiveError (errors.Is).
var ErrNotActive = &NotActiveError{}

func (e *NotActiveError) Is(target error) bool { return target == ErrNotActive }

// RefuseUnlessActiveIn refuses with NotActiveError unless the project is
// active. It holds the project row FOR SHARE (a caller that already holds
// it FOR UPDATE keeps that), so a lifecycle change waits for the door's
// transaction, and a door waits for the change.
func RefuseUnlessActiveIn(ctx context.Context, tx *database.Tx, id ids.ID) error {
	if _, err := queries(tx).ProjectRowForShare(ctx, id); err != nil {
		return err
	}
	l, err := LifecycleIn(ctx, tx, id)
	if err != nil {
		return err
	}
	if l.State != Active {
		return &NotActiveError{ProjectID: id, State: l.State}
	}
	return nil
}

// RefuseUnlessActive refuses, without a lock, new work in a project that
// is not active, so a person hears that rather than whatever else the
// request would first find missing. The door's admission decides it again
// under its lock (RefuseUnlessActiveIn).
func RefuseUnlessActive(ctx context.Context, q database.Queryer, id ids.ID) error {
	l, err := LifecycleIn(ctx, q, id)
	if err != nil {
		return err
	}
	if l.State != Active {
		return &NotActiveError{ProjectID: id, State: l.State}
	}
	return nil
}

// lockedLifecycleIn locks the project row FOR UPDATE and reads where it
// stands: every lifecycle change starts here.
func lockedLifecycleIn(ctx context.Context, tx *database.Tx, id ids.ID) (Lifecycle, error) {
	if _, err := queries(tx).ProjectByIDForUpdate(ctx, id); err != nil {
		return Lifecycle{}, err
	}
	return LifecycleIn(ctx, tx, id)
}

// LockedLifecycleIn is lockedLifecycleIn for an owner that decides a
// change under the row lock (the deletion pass, the purge admission).
func LockedLifecycleIn(ctx context.Context, tx *database.Tx, id ids.ID) (Lifecycle, error) {
	return lockedLifecycleIn(ctx, tx, id)
}

// ErrLifecycle refuses a lifecycle change the project's state does not
// allow; its words say why.
func lifecycleRefusal(format string, args ...any) error {
	return fault.New(fault.Conflict, fmt.Sprintf(format, args...))
}

// MarkDeletingIn moves an active project to deleting at the time at, by
// the clock that will judge its grace, under the scoped system binding the
// deletion runs under. A deleting project is re-confirmed instead: its
// stall is cleared and it runs under the new binding. who is the requester
// as the caller resolved them (identity.ActorIn).
func MarkDeletingIn(ctx context.Context, tx *database.Tx, id ids.ID, who access.Actor, bindingID ids.ID, synthetic bool, at time.Time) error {
	l, err := lockedLifecycleIn(ctx, tx, id)
	if err != nil {
		return err
	}
	switch l.State {
	case Active:
		return queries(tx).InsertProjectLifecycle(ctx, projectsql.InsertProjectLifecycleParams{ProjectID: id, State: Deleting, RequestedBy: who.Name, RequestedByPrincipal: who.Principal, RequestedByVia: who.Via, BindingID: bindingID, Synthetic: synthetic, RequestedAt: at})
	case Deleting:
		n, err := queries(tx).ReconfirmDeletion(ctx, projectsql.ReconfirmDeletionParams{ProjectID: id, RequestedBy: who.Name, RequestedByPrincipal: who.Principal, RequestedByVia: who.Via, BindingID: bindingID})
		if err == nil && n != 1 {
			err = errors.New("the deletion changed while it was re-confirmed")
		}
		return err
	}
	return &NotActiveError{ProjectID: id, State: l.State}
}

// CancelDeletionIn returns a deleting project to active, only before its
// first take-down was admitted.
func CancelDeletionIn(ctx context.Context, tx *database.Tx, id ids.ID) (Lifecycle, error) {
	l, err := lockedLifecycleIn(ctx, tx, id)
	if err != nil {
		return l, err
	}
	if l.State != Deleting {
		return l, lifecycleRefusal("the project is %s, not being deleted", l.State)
	}
	if l.TakeDownsStarted {
		return l, lifecycleRefusal("its deployments are being taken down, so its deletion can no longer be cancelled")
	}
	return l, deleteLifecycleIn(ctx, tx, id)
}

func deleteLifecycleIn(ctx context.Context, tx *database.Tx, id ids.ID) error {
	n, err := queries(tx).DeleteProjectLifecycle(ctx, id)
	if err == nil && n != 1 {
		err = errors.New("the project's lifecycle changed")
	}
	return err
}

// NoteTakeDownsStartedIn records that a deletion admitted its first
// take-down: cancelling is closed from here.
func NoteTakeDownsStartedIn(ctx context.Context, tx *database.Tx, id ids.ID) error {
	_, err := queries(tx).NoteTakeDownsStarted(ctx, id)
	return err
}

// StallDeletionIn records why a deletion waits on a person (a take-down
// that failed or is unknown, a binding revoked). It waits until re-confirmed.
func StallDeletionIn(ctx context.Context, tx *database.Tx, id ids.ID, why string) error {
	_, err := queries(tx).StallDeletion(ctx, projectsql.StallDeletionParams{ProjectID: id, Stalled: why})
	return err
}

// MarkDeletedIn moves a deleting project to deleted, kept until keptUntil.
func MarkDeletedIn(ctx context.Context, tx *database.Tx, id ids.ID, at, keptUntil time.Time) error {
	n, err := queries(tx).MarkProjectDeleted(ctx, projectsql.MarkProjectDeletedParams{ProjectID: id, At: sql.NullTime{Time: at, Valid: true}, KeptUntil: sql.NullTime{Time: keptUntil, Valid: true}})
	if err == nil && n != 1 {
		err = lifecycleRefusal("the project is no longer being deleted")
	}
	return err
}

// RestoreIn returns a deleted project to active, only until its first
// purge was admitted. Both decide under the project row lock.
func RestoreIn(ctx context.Context, tx *database.Tx, id ids.ID) (Lifecycle, error) {
	l, err := lockedLifecycleIn(ctx, tx, id)
	if err != nil {
		return l, err
	}
	if l.State != Deleted {
		return l, lifecycleRefusal("the project is %s, not deleted", l.State)
	}
	if l.PurgeStartedAt.Valid {
		return l, lifecycleRefusal("purge has started; restoring is no longer available")
	}
	return l, deleteLifecycleIn(ctx, tx, id)
}

// StartPurgeIn records, under the row lock the caller took
// (LockedLifecycleIn), that a purge was admitted: restoring is closed for
// good. A later attempt keeps the first time.
func StartPurgeIn(ctx context.Context, tx *database.Tx, id ids.ID, at time.Time) error {
	n, err := queries(tx).StartPurge(ctx, projectsql.StartPurgeParams{ProjectID: id, At: sql.NullTime{Time: at, Valid: true}})
	if err == nil && n != 1 {
		err = lifecycleRefusal("the project is not deleted")
	}
	return err
}

// DeletingIn lists the projects being deleted.
func DeletingIn(ctx context.Context, q database.Queryer) ([]Lifecycle, error) {
	rows, err := queries(q).DeletingProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Lifecycle, 0, len(rows))
	for _, r := range rows {
		out = append(out, lifecycleRecord(r))
	}
	return out, nil
}

// PurgesDueIn lists the deleted projects whose kept period has ended.
func PurgesDueIn(ctx context.Context, q database.Queryer, now time.Time) ([]Lifecycle, error) {
	rows, err := queries(q).PurgesDue(ctx, sql.NullTime{Time: now, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]Lifecycle, 0, len(rows))
	for _, r := range rows {
		out = append(out, lifecycleRecord(r))
	}
	return out, nil
}

// NotActiveProject is a project that is not active, with its address.
type NotActiveProject struct {
	Lifecycle
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// NotActive lists every project that is not active, for the instance's
// administrators.
func (s *Projects) NotActive(ctx context.Context) ([]NotActiveProject, error) {
	rows, err := queries(s.db).LifecyclesOf(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]NotActiveProject, 0, len(rows))
	for _, r := range rows {
		out = append(out, NotActiveProject{Lifecycle: lifecycleRecord(r.ProjectLifecycle), Slug: r.Slug, Name: r.Name})
	}
	return out, nil
}

// Lifecycle is a project's lifecycle.
func (s *Projects) Lifecycle(ctx context.Context, id ids.ID) (Lifecycle, error) {
	return LifecycleIn(ctx, s.db, id)
}

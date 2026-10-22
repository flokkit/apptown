package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/ids"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// A deleted project's cleanup moves through these states. Pending holds the
// slug; done releases it; needs-attention holds it until a person retries.
const (
	CleanupPending        = "pending"
	CleanupDone           = "done"
	CleanupNeedsAttention = "needs-attention"
)

// ErrSlugHeldByCleanup refuses a slug whose previous project is still being
// cleaned up: its containers, volumes and paths are named by that slug.
var ErrSlugHeldByCleanup = fault.New(fault.Conflict, "a deleted project with that address is still being cleaned up; the address is free once its cleanup is done")

// ErrCleanupNotRetryable refuses a retry of a cleanup that is not waiting
// for a person.
var ErrCleanupNotRetryable = fault.New(fault.Conflict, "that project's cleanup is not waiting for a retry")

// Deletion is a deleted project's cleanup: the plan committed with the
// deletion and how far it got.
type Deletion struct {
	ProjectID   ids.ID `json:"projectId"`
	Slug        string `json:"slug"`
	RequestedBy string `json:"requestedBy"`
	// RequestedByPrincipal is the requester's principal; zero when not resolved to a person.
	RequestedByPrincipal ids.ID          `json:"requestedByPrincipal,omitzero"`
	RequestedAt          time.Time       `json:"requestedAt"`
	Plan                 json.RawMessage `json:"plan"`
	State                string          `json:"state"`
	Detail               string          `json:"detail"`
	Attempts             int             `json:"attempts"`
	Round                int             `json:"round"`
	FinishedAt           *time.Time      `json:"finishedAt,omitempty"`
}

func deletionRecord(r projectsql.ProjectDeletion) Deletion {
	d := Deletion{ProjectID: r.ProjectID, Slug: r.Slug, RequestedBy: r.RequestedBy, RequestedByPrincipal: r.RequestedByPrincipal, RequestedAt: r.RequestedAt, Plan: json.RawMessage(r.Plan),
		State: r.State, Detail: r.Detail, Attempts: int(r.Attempts), Round: int(r.Round)}
	if r.FinishedAt.Valid {
		at := r.FinishedAt.Time
		d.FinishedAt = &at
	}
	return d
}

// RecordDeletionIn records a deleted project's cleanup plan in the deletion's
// own transaction, so the intent survives whatever happens after commit;
// at is the caller's clock; who is the requester as the caller resolved
// them (identity.ActorIn), since identity's tests build on this package.
func RecordDeletionIn(ctx context.Context, tx *database.Tx, id ids.ID, slug string, who access.Actor, plan any, at time.Time) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	return queries(tx).InsertProjectDeletion(ctx, projectsql.InsertProjectDeletionParams{
		ProjectID: id, Slug: slug, RequestedBy: who.Name, RequestedByPrincipal: who.Principal, RequestedByVia: who.Via, RequestedAt: at.UTC(), Plan: raw})
}

// requireSlugFreeIn runs after a write that claimed a slug, in its
// transaction. Checked after rather than before: a claim racing a deletion
// waits on the slug's unique index until the deletion commits, and only a
// statement after that wait sees the deletion's cleanup holding the name.
func requireSlugFreeIn(ctx context.Context, q database.Queryer, slug string) error {
	held, err := queries(q).SlugHeldByDeletion(ctx, slug)
	if err != nil {
		return err
	}
	if held {
		return ErrSlugHeldByCleanup
	}
	return nil
}

// Deletion reads one deleted project's cleanup.
func (s *Projects) Deletion(ctx context.Context, id ids.ID) (Deletion, error) {
	r, err := queries(s.db).ProjectDeletion(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Deletion{}, fault.New(fault.NotFound, "no deleted project with that id")
	}
	if err != nil {
		return Deletion{}, err
	}
	return deletionRecord(r), nil
}

// DeletedIn reports whether project id was deleted: its deletion is
// recorded. A project that never existed was not deleted.
func DeletedIn(ctx context.Context, q database.Queryer, id ids.ID) (bool, error) {
	_, err := queries(q).ProjectDeletion(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Deletions lists recent deleted projects' cleanups, newest first.
func (s *Projects) Deletions(ctx context.Context) ([]Deletion, error) {
	rows, err := queries(s.db).ProjectDeletions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Deletion, 0, len(rows))
	for _, r := range rows {
		out = append(out, deletionRecord(r))
	}
	return out, nil
}

// SettleDeletionAttempt records one cleanup attempt of a round. It applies
// only to a pending cleanup of that round, and reports whether it did: a
// stale job of an earlier round records nothing.
func (s *Projects) SettleDeletionAttempt(ctx context.Context, id ids.ID, round int, state, detail string) (bool, error) {
	n, err := queries(s.db).SettleProjectDeletionAttempt(ctx, projectsql.SettleProjectDeletionAttemptParams{
		ProjectID: id, Round: int32(round), State: state, Detail: detail, At: s.now()})
	return n == 1, err
}

// DeletionForUpdateIn reads one cleanup and holds it for the caller's
// transaction.
func DeletionForUpdateIn(ctx context.Context, tx *database.Tx, id ids.ID) (Deletion, error) {
	r, err := queries(tx).ProjectDeletionForUpdate(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Deletion{}, fault.New(fault.NotFound, "no deleted project with that id")
	}
	if err != nil {
		return Deletion{}, err
	}
	return deletionRecord(r), nil
}

// RestartDeletionIn starts a new round of an unfinished cleanup and returns
// its number. The caller decides that the cleanup may be restarted (it needs
// attention, or its queued job is no longer live); a finished cleanup is
// ErrCleanupNotRetryable.
func RestartDeletionIn(ctx context.Context, tx *database.Tx, id ids.ID) (int, error) {
	round, err := queries(tx).RestartProjectDeletion(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrCleanupNotRetryable
	}
	return int(round), err
}

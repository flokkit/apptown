package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/usage/internal/usagesql"
)

// Request supplies the workload attribution and selected provider/model for an
// inference receipt. The caller verifies these identities before [Requests.Begin].
type Request struct {
	Kind, Actor string
	// Who is the actor as the workload was admitted: the receipt records
	// its principal and way, not the name looked up again.
	Who             access.Actor
	ProjectID       ids.ID
	RunID           ids.ID
	StepID          ids.ID
	Attempt         int64
	SessionID       string
	Provider, Model string
}

// Requests is the supported API over inference_requests.
type Requests struct {
	db *database.DB
	// Now is the forge's clock, which stamps a receipt and judges a spend
	// line's pending time; nil is the real one.
	Now func() time.Time
}

func NewRequests(db *database.DB) *Requests { return &Requests{db: db} }

func (r *Requests) now() time.Time { return clock.Or(r.Now)() }

// Begin persists an open receipt and returns its ID. Callers must not dispatch
// upstream when it fails. It neither authorizes the request nor reserves a budget.
func (r *Requests) Begin(ctx context.Context, q Request) (ids.ID, error) {
	caller := q.Who
	row, err := usagesql.New(r.db.SQL()).BeginRequest(ctx, usagesql.BeginRequestParams{ID: ids.New(r.now().UTC()), StartedAt: r.now().UTC(), Kind: q.Kind, Actor: q.Actor, ActorPrincipal: caller.Principal, ActorVia: caller.Via, ProjectID: q.ProjectID, RunID: q.RunID, StepID: q.StepID, Attempt: q.Attempt, SessionID: q.SessionID, Provider: q.Provider, Model: q.Model})
	return row.ID, err
}

// Finish closes a receipt with the provider's status, the outcome word and
// the usage as observed (JSON). The caller owns request lifetime: this operation
// replaces any previous outcome and does not enforce one-time settlement. A
// nonexistent ID is not reported as an error by the underlying update. The
// receipt records a call already made, so it is written though ctx ends.
func (r *Requests) Finish(ctx context.Context, id ids.ID, httpStatus int, outcome, usageJSON string) error {
	return usagesql.New(r.db.SQL()).FinishRequest(context.WithoutCancel(ctx), usagesql.FinishRequestParams{FinishedAt: sql.NullTime{Time: r.now().UTC(), Valid: true}, HttpStatus: int64(httpStatus), Outcome: outcome, UsageJson: json.RawMessage(usageJSON), ID: id})
}

// MarkInterrupted closes every receipt a restart left open and reports how
// many; no final token count is invented for them. Every forge call's spend
// line still pending is locked in as unknown in the same transaction: the
// restart cut those calls off, so none of them can settle any more. Call it
// before admitting new requests at startup, not while another gateway is
// serving on the same database.
func (r *Requests) MarkInterrupted(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.InTx(ctx, func(tx *database.Tx) error {
		q := usagesql.New(tx.SQL())
		var err error
		if n, err = q.InterruptRequests(ctx, sql.NullTime{Time: r.now().UTC(), Valid: true}); err != nil {
			return err
		}
		_, err = q.LockInAllPendingLines(ctx, NeverSettled)
		return err
	})
	return n, err
}

// NeverSettled is the reason on a forge call's spend line that was sent but
// never settled: its time ran out, or a restart cut the call off.
const NeverSettled = "the call was sent but its spend was never settled"

// CallKey is the observation key of a forge call's spend line: one line per
// receipt, so one per provider call.
func CallKey(receipt ids.ID) string { return "inference:" + receipt.String() }

// OpenCall writes a forge model call's receipt and its spend line in one
// transaction, before the call is sent. The line is e's attribution with
// nothing counted yet, keyed CallKey(receipt), and pending for the given
// time: unsettled after that, it counts as unknown spend. Callers must not
// send the call when this fails.
func (r *Requests) OpenCall(ctx context.Context, q Request, e Entry, pending time.Duration) (receipt ids.ID, err error) {
	err = r.db.InTx(ctx, func(tx *database.Tx) error {
		qs := usagesql.New(tx.SQL())
		at := r.now().UTC()
		caller, err := identity.ActorIn(ctx, tx, q.Actor)
		if err != nil {
			return err
		}
		spender := caller
		if e.Actor != q.Actor {
			if spender, err = identity.ActorIn(ctx, tx, e.Actor); err != nil {
				return err
			}
		}
		row, err := qs.BeginRequest(ctx, usagesql.BeginRequestParams{ID: ids.New(at), StartedAt: at, Kind: q.Kind, Actor: q.Actor, ActorPrincipal: caller.Principal, ActorVia: caller.Via, ProjectID: q.ProjectID, RunID: q.RunID, StepID: q.StepID, Attempt: q.Attempt, SessionID: q.SessionID, Provider: q.Provider, Model: q.Model})
		if err != nil {
			return err
		}
		receipt = row.ID
		return qs.OpenCallLine(ctx, usagesql.OpenCallLineParams{ID: ids.New(at), At: at, Kind: e.Kind, ProjectID: e.ProjectID, RunID: e.RunID, Actor: e.Actor, ActorPrincipal: spender.Principal, ActorVia: spender.Via,
			Provider: e.Provider, Model: e.Model, Observation: CallKey(receipt), PendingUntil: at.Add(pending)})
	})
	return receipt, err
}

// SettleCall closes a forge call's receipt and settles its spend line in
// one transaction. The line is settled only while it is still pending and
// its time has not run out; settled reports whether it was. A line already
// locked in as unknown stays unknown, and a replay changes nothing.
// incomplete, when not "", says why u may be lower than what was spent.
func (r *Requests) SettleCall(ctx context.Context, receipt ids.ID, httpStatus int, outcome, usageJSON string, u Totals, incomplete string) (settled bool, err error) {
	err = r.db.InTx(ctx, func(tx *database.Tx) error {
		settled, err = settleCallIn(ctx, tx, receipt, httpStatus, outcome, usageJSON, u, incomplete, r.now())
		return err
	})
	return settled, err
}

// settleCallIn is SettleCall's work inside the caller's transaction q. The
// UPDATE of the spend line takes its row lock, which is what decides a race
// with LockInUnknownIn.
func settleCallIn(ctx context.Context, q database.Queryer, receipt ids.ID, httpStatus int, outcome, usageJSON string, u Totals, incomplete string, at time.Time) (bool, error) {
	qs := usagesql.New(q.SQL())
	if err := qs.FinishRequest(ctx, usagesql.FinishRequestParams{FinishedAt: sql.NullTime{Time: at.UTC(), Valid: true}, HttpStatus: int64(httpStatus), Outcome: outcome, UsageJson: json.RawMessage(usageJSON), ID: receipt}); err != nil {
		return false, err
	}
	n, err := qs.SettleCallLine(ctx, usagesql.SettleCallLineParams{Now: at, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite,
		Total: u.Total, CostMicros: u.CostMicros, Incomplete: incomplete, Observation: CallKey(receipt)})
	return n == 1, err
}

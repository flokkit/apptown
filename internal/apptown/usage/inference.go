package usage

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/usage/internal/usagesql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// Requests is the supported API over the spend lines of AppTown's own model
// calls: opened before a call is sent, settled from its answer, locked in as
// unknown when it never settles.
type Requests struct {
	db *database.DB
	// clock is AppTown's clock, which judges a spend line's pending time; nil
	// is the real one.
	clock func() time.Time
}

func NewRequests(db *database.DB) *Requests { return &Requests{db: db} }

func (r *Requests) now() time.Time { return clock.Or(r.clock)() }

// MarkInterrupted locks in as unknown every AppTown call's spend line still
// pending and reports how many: the restart cut those calls off, so none of
// them can settle any more. Call it before admitting new requests at
// startup, not while another gateway is serving on the same database.
func (r *Requests) MarkInterrupted(ctx context.Context) (int64, error) {
	return usagesql.New(r.db.SQL()).LockInAllPendingLines(ctx, NeverSettled)
}

// NeverSettled is the reason on an AppTown call's spend line that was sent but
// never settled: its time ran out, or a restart cut the call off.
const NeverSettled = "the call was sent but its spend was never settled"

// CallKey is the observation key of an AppTown call's spend line: one line
// per provider call, keyed by the call's id.
func CallKey(call ids.ID) string { return "inference:" + call.String() }

// OpenCall writes an AppTown model call's spend line before the call is sent
// and returns the call's id. The line is e's attribution with nothing counted
// yet, keyed CallKey(call), and pending for the given time: unsettled after
// that, it counts as unknown spend. Callers must not send the call when this
// fails.
func (r *Requests) OpenCall(ctx context.Context, e Entry, pending time.Duration) (call ids.ID, err error) {
	at := r.now().UTC()
	spender, err := identity.ActorIn(ctx, r.db, e.Actor)
	if err != nil {
		return ids.Nil, err
	}
	call = ids.New(at)
	err = usagesql.New(r.db.SQL()).OpenCallLine(ctx, usagesql.OpenCallLineParams{ID: ids.New(at), At: at, Kind: e.Kind, ProjectID: e.ProjectID, RunID: e.RunID, Actor: e.Actor, ActorPrincipal: spender.Principal, ActorVia: spender.Via,
		Provider: e.Provider, Model: e.Model, Observation: CallKey(call), PendingUntil: at.Add(pending)})
	return call, err
}

// SettleCall settles an AppTown call's spend line. The line is settled only
// while it is still pending and its time has not run out; settled reports
// whether it was. A line already locked in as unknown stays unknown, and a
// replay changes nothing. incomplete, when not "", says why u may be lower
// than what was spent.
func (r *Requests) SettleCall(ctx context.Context, call ids.ID, u Totals, incomplete string) (settled bool, err error) {
	return settleCallIn(ctx, r.db, call, u, incomplete, r.now())
}

// settleCallIn is SettleCall's work on q, which may be the caller's
// transaction. The UPDATE of the spend line takes its row lock, which is what
// decides a race with LockInUnknownIn.
func settleCallIn(ctx context.Context, q database.Queryer, call ids.ID, u Totals, incomplete string, at time.Time) (bool, error) {
	n, err := usagesql.New(q.SQL()).SettleCallLine(ctx, usagesql.SettleCallLineParams{Now: at, Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite,
		Total: u.Total, CostMicros: u.CostMicros, Incomplete: incomplete, Observation: CallKey(call)})
	return n == 1, err
}

package usage

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/usage/internal/usagesql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// Totals is a sum of spending; the cost accumulates in dollars and is
// reported in micro-dollars.
type Totals struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Total      int64 `json:"total"`
	CostMicros int64 `json:"costMicros"`
	// Incomplete counts the observations whose spend was not wholly known:
	// when it is not zero, the totals are "at least".
	Incomplete int64   `json:"incomplete"`
	costUSD    float64 // accumulation only
}

func (u *Totals) Add(input, output, cacheRead, cacheWrite, total float64, costUSD float64) {
	u.Input += int64(input)
	u.Output += int64(output)
	u.CacheRead += int64(cacheRead)
	u.CacheWrite += int64(cacheWrite)
	u.Total += int64(total)
	u.costUSD += costUSD
	u.CostMicros = int64(u.costUSD * 1e6)
}

// Entry is one ledger row's identity: who spent, on what.
type Entry struct {
	Kind            string // step | console
	ProjectID       ids.ID
	RunID           ids.ID
	RootRunID       ids.ID
	StepID          ids.ID
	WorkflowName    string
	WorkflowVersion int64
	SessionID       string
	Actor           string
	// Who, when the caller knows it, is the spender as admitted (a chat's
	// opener and the chat): recorded as is, never the name looked up again.
	Who             access.Actor
	Provider, Model string
}

// Ledger is the supported API over the ledger and its cursors.
type Ledger struct {
	db *database.DB
	// Now is AppTown's clock, which stamps an observation and judges a
	// report's window and a pending line's time; nil is the real one.
	Now func() time.Time
}

func New(db *database.DB) *Ledger { return &Ledger{db: db} }

func (l *Ledger) now() time.Time { return clock.Or(l.Now)() }

// Record appends one observation. A complete observation with nothing
// spent (Total and CostMicros both 0) is a no-op. incomplete, when not "",
// says why u may be lower than what was spent (a call that ended after it
// was sent, an answer with no usage, tokens with no price); such an
// observation is recorded even at zero, so unknown spend is never read as
// nothing spent. The caller supplies trusted attribution and owns
// deduplication: repeated calls append repeated rows. What is read from a
// transcript is recorded with its cursor by [Ledger.RecordHarvest].
// Spend already incurred is recorded though ctx ends.
func (l *Ledger) Record(ctx context.Context, e Entry, u Totals, incomplete string) error {
	return l.recordIn(context.WithoutCancel(ctx), l.db, e, u, incomplete)
}

func (l *Ledger) recordIn(ctx context.Context, q database.Queryer, e Entry, u Totals, incomplete string) error {
	if u.Total == 0 && u.CostMicros == 0 && incomplete == "" {
		return nil
	}
	spender := e.Who
	if spender.Name == "" {
		var err error
		if spender, err = identity.ActorIn(ctx, q, e.Actor); err != nil {
			return err
		}
	}
	return usagesql.New(q.SQL()).RecordUsage(ctx, usagesql.RecordUsageParams{ID: ids.New(l.now().UTC()),
		At: l.now().UTC(), Kind: e.Kind, ProjectID: e.ProjectID, RunID: e.RunID, RootRunID: e.RootRunID, StepID: e.StepID,
		WorkflowName: e.WorkflowName, WorkflowVersion: e.WorkflowVersion, SessionID: e.SessionID, Actor: e.Actor, ActorPrincipal: spender.Principal, ActorVia: spender.Via, Provider: e.Provider, Model: e.Model,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Total: u.Total, CostMicros: u.CostMicros,
		Incomplete: incomplete,
	})
}

// RecordOnce records one observation unless one with the same key was
// already recorded, so replaying an invocation's settlement (after a crash,
// or a second read of the same output) counts its usage once. The key names
// the observation, such as one agent invocation of one step attempt; an empty
// key is refused, as it would identify nothing.
//
// incomplete, when not "", says why u may be lower than what was spent (a
// transcript that could not be read whole); it is stored with the totals,
// and the first record of a key stands, so a replay cannot turn an
// incomplete observation into a complete one. A complete observation with
// Total == 0 is a no-op; an incomplete one is
// recorded even at zero, so it never reads as "nothing was spent".
func (l *Ledger) RecordOnce(ctx context.Context, key string, e Entry, u Totals, incomplete string) error {
	if key == "" {
		return errors.New("usage: an observation recorded once needs a key")
	}
	if u.Total == 0 && incomplete == "" {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	spender, err := identity.ActorIn(ctx, l.db, e.Actor)
	if err != nil {
		return err
	}
	return usagesql.New(l.db.SQL()).RecordUsageOnce(ctx, usagesql.RecordUsageOnceParams{ID: ids.New(l.now().UTC()),
		At: l.now().UTC(), Kind: e.Kind, ProjectID: e.ProjectID, RunID: e.RunID, RootRunID: e.RootRunID, StepID: e.StepID,
		WorkflowName: e.WorkflowName, WorkflowVersion: e.WorkflowVersion, SessionID: e.SessionID, Actor: e.Actor, ActorPrincipal: spender.Principal, ActorVia: spender.Via, Provider: e.Provider, Model: e.Model,
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Total: u.Total, CostMicros: u.CostMicros,
		Observation: key, Incomplete: incomplete,
	})
}

// Observation is what was recorded for one keyed observation.
type Observation struct {
	Totals Totals
	// Incomplete says why Totals may be lower than what was spent; "" when
	// the observation was complete.
	Incomplete string
}

// Observed reads the observation recorded under key; false when none was.
func (l *Ledger) Observed(ctx context.Context, key string) (Observation, bool, error) {
	r, err := usagesql.New(l.db.SQL()).ObservationByKey(ctx, key)
	found, err := database.Lookup(err)
	if err != nil || !found {
		return Observation{}, false, err
	}
	return Observation{Totals: Totals{Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite, Total: r.Total, CostMicros: r.CostMicros}, Incomplete: r.Incomplete}, true, nil
}

// Cursor is how far a session file has been read; 0 when never.
func (l *Ledger) Cursor(ctx context.Context, path string) (int64, error) {
	r, err := usagesql.New(l.db.SQL()).Cursor(ctx, path)
	_, err = database.Lookup(err)
	return r.ByteOffset, err
}

// Harvested is one observation read from a transcript.
type Harvested struct {
	Entry  Entry
	Totals Totals
}

// RecordHarvest records what was read from a transcript and moves its cursor
// to offset, in one transaction: the cursor advances only past spend that was
// stored, and a failed cursor write stores none of it. Spend already
// incurred is recorded though ctx ends.
func (l *Ledger) RecordHarvest(ctx context.Context, path string, offset int64, rows []Harvested) error {
	ctx = context.WithoutCancel(ctx)
	return l.db.InTx(ctx, func(tx *database.Tx) error {
		for _, r := range rows {
			if err := l.recordIn(ctx, tx, r.Entry, r.Totals, ""); err != nil {
				return err
			}
		}
		return usagesql.New(tx.SQL()).SetCursor(ctx, usagesql.SetCursorParams{Path: path, ByteOffset: offset})
	})
}

// RunTotal sums the spending of the run trees rooted at rootRunIDs: one
// run's, or a change's across every run it has had (its spending does not
// end with the run it is on now).
func (l *Ledger) RunTotal(ctx context.Context, rootRunIDs ...ids.ID) (total, costMicros int64, err error) {
	if len(rootRunIDs) == 0 {
		return 0, 0, nil
	}
	r, err := usagesql.New(l.db.SQL()).RunTotal(ctx, rootRunIDs)
	return r.Total, r.CostMicros, err
}

// Line is one row of a rollup.
type Line struct {
	Key                              string
	Input, Output, Total, CostMicros int64
}

// DayKind is one day's spending by kind, for the stacked chart.
type DayKind struct {
	Day, Kind         string
	Total, CostMicros int64
}

// Report is the usage page: totals and the rollups over a window.
type Report struct {
	WindowDays int
	Totals     Totals
	ByKind     []Line
	ByModel    []Line
	ByDay      []Line // chronological
	ByDayKind  []DayKind
	ByWorkflow []Line // when scoped to a project
	ByProject  []Line // when not
}

// Summary is the rollup over the last windowDays, scoped to a project
// and a workflow when given. Zero projectID and an empty workflow remove their
// respective filters; the caller must authorize the scope. Dimension lists are
// capped at 50 groups, so they need not sum to Totals. A failed query returns an
// error and a possibly partial Report, which must not be treated as complete.
func (l *Ledger) Summary(ctx context.Context, windowDays int, projectID ids.ID, workflow string) (Report, error) {
	scope := Scope{Instance: true}
	if !projectID.IsZero() {
		scope = Scope{Projects: []ids.ID{projectID}}
	}
	return l.summary(ctx, windowDays, scope, workflow, !projectID.IsZero())
}

// Scope is the usage a report covers: the whole instance, including usage
// attributed to no project, or only the listed projects. An empty list
// selects nothing, never the instance.
type Scope struct {
	Instance bool
	Projects []ids.ID
}

// summary reads one report; byWorkflow breaks spending down by workflow
// (one project's report), otherwise by project.
func (l *Ledger) summary(ctx context.Context, windowDays int, scope Scope, workflow string, byWorkflow bool) (Report, error) {
	rep := Report{WindowDays: windowDays}
	now := l.now().UTC()
	since := now.AddDate(0, 0, -windowDays)
	projects := scope.Projects
	if projects == nil {
		projects = []ids.ID{}
	}
	q := usagesql.New(l.db.SQL())
	totals, err := q.UsageTotals(ctx, usagesql.UsageTotalsParams{Now: now, Since: since, WholeInstance: scope.Instance, ProjectIds: projects, Workflow: workflow})
	if err != nil {
		return rep, err
	}
	rep.Totals = Totals{Input: totals.Input, Output: totals.Output, CacheRead: totals.CacheRead, CacheWrite: totals.CacheWrite, Total: totals.Total, CostMicros: totals.CostMicros, Incomplete: totals.Incomplete}
	group := func(dimension string) ([]Line, error) {
		rows, err := q.UsageGroups(ctx, usagesql.UsageGroupsParams{Dimension: dimension, Since: since, WholeInstance: scope.Instance, ProjectIds: projects, Workflow: workflow})
		kept := make([]Line, 0, len(rows))
		for _, r := range rows {
			if r.Key != "" {
				kept = append(kept, Line{Key: r.Key, Input: r.Input, Output: r.Output, Total: r.Total, CostMicros: r.CostMicros})
			}
		}
		return kept, err
	}
	if rep.ByKind, err = group("kind"); err != nil {
		return rep, err
	}
	if rep.ByModel, err = group("model"); err != nil {
		return rep, err
	}
	if rep.ByDay, err = group("day"); err != nil {
		return rep, err
	}
	sort.Slice(rep.ByDay, func(i, j int) bool { return rep.ByDay[i].Key < rep.ByDay[j].Key })
	days, err := q.UsageDaysByKind(ctx, usagesql.UsageDaysByKindParams{Since: since, WholeInstance: scope.Instance, ProjectIds: projects, Workflow: workflow})
	if err != nil {
		return rep, err
	}
	rep.ByDayKind = make([]DayKind, 0, len(days))
	for _, d := range days {
		rep.ByDayKind = append(rep.ByDayKind, DayKind{Day: d.Day, Kind: d.Kind, Total: d.Total, CostMicros: d.CostMicros})
	}
	if byWorkflow {
		rep.ByWorkflow, err = group("workflow")
	} else {
		rep.ByProject, err = group("project")
	}
	return rep, err
}

// ProjectsTotal is every observation of the listed projects, whenever it
// was recorded, whatever kind or run (agents, checks, narratives, briefs):
// what those projects cost, read through q (a caller's transaction, so the
// read needs no second connection while it holds one). Incomplete counts
// the observations whose spend was not wholly known; the total is then "at
// least". An AppTown call's pending line whose time has run out counts as
// incomplete; one still in time counts nothing yet. No projects is nothing.
func (l *Ledger) ProjectsTotal(ctx context.Context, q database.Queryer, projectIDs ...ids.ID) (Totals, error) {
	if len(projectIDs) == 0 {
		return Totals{}, nil
	}
	if q == nil {
		q = l.db
	}
	r, err := usagesql.New(q.SQL()).ProjectsTotal(ctx, usagesql.ProjectsTotalParams{Now: l.now(), ProjectIds: projectIDs})
	return Totals{Input: r.Input, Output: r.Output, Total: r.Total, CostMicros: r.CostMicros, Incomplete: r.Incomplete}, err
}

// LockInUnknownIn locks in the listed projects' expired pending lines as
// unknown spend, then counts the projects' unknown lines, all through q (the
// caller's transaction). The count includes only lines that can never
// change again, so an acknowledgement saved from it can never come to cover
// a later unknown charge. Each locked-in row is decided against a
// concurrent settlement by its row lock: a settlement that updated the row
// first wins, and one that comes after changes nothing. q must be a
// transaction under Read Committed, so the count, a new statement, sees
// the outcome of any wait.
func (l *Ledger) LockInUnknownIn(ctx context.Context, q database.Queryer, projectIDs ...ids.ID) (int64, error) {
	if len(projectIDs) == 0 {
		return 0, nil
	}
	qs := usagesql.New(q.SQL())
	if err := qs.LockInExpiredLines(ctx, usagesql.LockInExpiredLinesParams{Now: l.now(), Incomplete: NeverSettled, ProjectIds: projectIDs}); err != nil {
		return 0, err
	}
	return qs.LockedInIncomplete(ctx, projectIDs)
}

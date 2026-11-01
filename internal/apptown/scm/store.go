package scm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/scm/internal/scmsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

type mergeStore struct {
	db    *database.DB
	clock func() time.Time // AppTown's clock; nil is the real one
}
type preparedMerge struct {
	db     *database.DB
	work   Claim
	source Source
	intent intent
	clock  func() time.Time
}

func (p *preparedMerge) now() time.Time { return clock.Or(p.clock)() }

type denied struct{ refusal Refusal }

func (e *denied) Error() string { return e.refusal.Reason }

func readIntent(ctx context.Context, db database.Queryer, runID ids.ID) (intent, error) {
	r, err := scmsql.New(db.SQL()).MergeByRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return intent{State: Idle}, nil
	}
	if err != nil {
		return intent{}, err
	}
	m := intent{Operation: r.Operation, Expected: r.Expected, Result: r.Result}
	if err := m.State.Scan(r.State); err != nil {
		return intent{}, err
	}
	if m.State == Idle || m.State == Pending && (m.Operation == "" || m.Expected == "" || m.Result != "") || m.State == Recorded && (m.Operation == "" || m.Expected == "" || m.Result == "") {
		return intent{}, fmt.Errorf("run %d has invalid merge operation metadata", runID)
	}
	return m, nil
}
func (s mergeStore) prepare(ctx context.Context, work Claim, purpose Purpose) (*preparedMerge, error) {
	if work == nil {
		return nil, ErrCaller
	}
	source := work.Source()
	if err := source.validate(); err != nil {
		return nil, err
	}
	var refusal Refusal
	result, err := database.Transact(ctx, s.db, func(tx *database.Tx) (*preparedMerge, error) {
		var err error
		refusal, err = work.CheckIn(ctx, tx, purpose)
		if err != nil {
			return nil, err
		}
		if refusal.refused() {
			return nil, nil
		}
		current, err := readIntent(ctx, tx, source.RunID)
		if err != nil {
			return nil, err
		}
		return &preparedMerge{s.db, work, source, current, s.clock}, nil
	})
	if err != nil {
		return nil, err
	}
	if refusal.refused() {
		return nil, &denied{refusal}
	}
	return result, nil
}
func (p *preparedMerge) checkIn(ctx context.Context, tx *database.Tx, purpose Purpose) (intent, Refusal, error) {
	if p.work.Source() != p.source {
		return intent{}, Refusal{}, ErrChanged
	}
	refusal, err := p.work.CheckIn(ctx, tx, purpose)
	if err != nil || refusal.refused() {
		return intent{}, refusal, err
	}
	current, err := readIntent(ctx, tx, p.source.RunID)
	return current, Refusal{}, err
}
func (p *preparedMerge) lock(ctx context.Context, tx *database.Tx) (intent, error) {
	current, refusal, err := p.checkIn(ctx, tx, Observe)
	if err != nil {
		return intent{}, err
	}
	if refusal.refused() {
		return intent{}, &denied{refusal}
	}
	return current, nil
}
func requireWrite(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrChanged
	}
	return nil
}
func writeIntent(ctx context.Context, tx *database.Tx, runID ids.ID, previous, next intent) error {
	q := scmsql.New(tx.SQL())
	switch {
	case next.State == Idle:
		return requireWrite(q.RemoveMerge(ctx, scmsql.RemoveMergeParams{RunID: runID, State: previous.State.String(), Operation: previous.Operation, Expected: previous.Expected, Result: previous.Result}))
	case previous.State == Idle:
		return requireWrite(q.BeginMerge(ctx, scmsql.BeginMergeParams{RunID: runID, State: next.State.String(), Operation: next.Operation, Expected: next.Expected, Result: next.Result}))
	default:
		return requireWrite(q.ReplaceMerge(ctx, scmsql.ReplaceMergeParams{RunID: runID, State: next.State.String(), Operation: next.Operation, Expected: next.Expected, Result: next.Result, PreviousState: previous.State.String(), PreviousOperation: previous.Operation, PreviousExpected: previous.Expected, PreviousResult: previous.Result}))
	}
}

func (p *preparedMerge) begin(ctx context.Context, operation, expected string) (*preparedMerge, error) {
	var refusal Refusal
	result, err := database.Transact(ctx, p.db, func(tx *database.Tx) (*preparedMerge, error) {
		current, blocked, err := p.checkIn(ctx, tx, NewEffect)
		if err != nil {
			return nil, err
		}
		refusal = blocked
		// Commit a new Work policy pause even though no intent is written.
		if refusal.refused() {
			return nil, nil
		}
		if current != p.intent {
			return nil, ErrChanged
		}
		next, err := current.begin(operation, expected)
		if err != nil {
			return nil, err
		}
		if err := writeIntent(ctx, tx, p.source.RunID, current, next); err != nil {
			return nil, err
		}
		result := *p
		result.intent = next
		return &result, nil
	})
	if err != nil {
		return nil, err
	}
	if refusal.refused() {
		return nil, &denied{refusal}
	}
	return result, nil
}
func (p *preparedMerge) settle(ctx context.Context, sha string) error {
	move, err := p.mainMove(ctx)
	if err != nil {
		return err
	}
	_, err = database.Transact(ctx, p.db, func(tx *database.Tx) (struct{}, error) {
		current, err := p.lock(ctx, tx)
		if err != nil {
			return struct{}{}, err
		}
		if current.State == Recorded && current.Operation == p.intent.Operation && current.Expected == p.intent.Expected && current.Result == sha {
			return struct{}{}, nil
		}
		if current != p.intent {
			return struct{}{}, ErrChanged
		}
		next, err := current.settle(sha)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, p.recordIn(ctx, tx, current, next, "", move)
	})
	return err
}
func (p *preparedMerge) observe(ctx context.Context, operation, sha string) error {
	move, err := p.mainMove(ctx)
	if err != nil {
		return err
	}
	_, err = database.Transact(ctx, p.db, func(tx *database.Tx) (struct{}, error) {
		current, err := p.lock(ctx, tx)
		if err != nil {
			return struct{}{}, err
		}
		if current != p.intent {
			return struct{}{}, ErrChanged
		}
		if current.State == Recorded && current.Operation == operation {
			if current.Result != sha {
				return struct{}{}, ErrChanged
			}
			return struct{}{}, nil
		}
		next, err := current.begin(operation, sha)
		if err != nil {
			return struct{}{}, err
		}
		next, err = next.settle(sha)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, p.recordIn(ctx, tx, current, next, "existing-merge", move)
	})
	return err
}
func (p *preparedMerge) cancel(ctx context.Context, reason string) error {
	_, err := database.Transact(ctx, p.db, func(tx *database.Tx) (struct{}, error) {
		current, err := p.lock(ctx, tx)
		if err != nil {
			return struct{}{}, err
		}
		if current != p.intent {
			return struct{}{}, ErrChanged
		}
		next, err := current.cancel()
		if err != nil {
			return struct{}{}, err
		}
		receipt := Receipt{Kind: cancelled, Operation: current.Operation, Reason: reason}
		if _, err := receipt.Event(); err != nil {
			return struct{}{}, err
		}
		if err := writeIntent(ctx, tx, p.source.RunID, current, next); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, p.work.RecordIn(ctx, tx, receipt)
	})
	return err
}

// mainMove reads the hub's main back for the settlement. Every caller holds
// the project's repository lock (Merge, Inspect), so main cannot move between
// this reading and the settlement's transaction.
func (p *preparedMerge) mainMove(ctx context.Context) (mainMove, error) {
	if !p.source.repositoryBacked() {
		return mainMove{}, nil
	}
	return PrepareMainMove(ctx, p.db, p.source.Repository, p.source.ProjectID, p.source.DefaultBranch)
}

// recordIn settles a merge: the run's merge slot, Work's receipt and landed
// commit, and the project's main as read back (which can be later than this
// merge's result when recovery settles an older merge).
func (p *preparedMerge) recordIn(ctx context.Context, tx *database.Tx, previous, next intent, via string, move mainMove) error {
	receipt := Receipt{Kind: Completed, Operation: next.Operation, Branch: p.source.Branch, SHA: next.Result, Via: via}
	if via == "existing-merge" && p.source.MR.IID != 0 {
		receipt.Via, receipt.IID = "gitlab-mr", p.source.MR.IID
	}
	if _, err := receipt.Event(); err != nil {
		return err
	}
	if err := writeIntent(ctx, tx, p.source.RunID, previous, next); err != nil {
		return err
	}
	if err := p.work.RecordIn(ctx, tx, receipt); err != nil {
		return err
	}
	return move.RecordIn(ctx, tx, ViaLanding, p.source.RunID, p.now())
}
func (p *preparedMerge) revalidate(ctx context.Context) error {
	var refusal Refusal
	_, err := database.Transact(ctx, p.db, func(tx *database.Tx) (struct{}, error) {
		current, blocked, err := p.checkIn(ctx, tx, NewEffect)
		if err != nil {
			return struct{}{}, err
		}
		refusal = blocked
		if refusal.refused() {
			return struct{}{}, nil
		}
		if current != p.intent {
			return struct{}{}, ErrChanged
		}
		return struct{}{}, nil
	})
	if err != nil {
		return err
	}
	if refusal.refused() {
		return &denied{refusal}
	}
	return nil
}

func (s mergeStore) pending(ctx context.Context) ([]ids.ID, error) {
	rows, err := scmsql.New(s.db.SQL()).PendingMerges(ctx, Pending.String())
	if err != nil {
		return nil, err
	}
	out := make([]ids.ID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RunID)
	}
	return out, err
}
func (p *preparedMerge) reportUnresolved(ctx context.Context, reason string) error {
	_, err := database.Transact(ctx, p.db, func(tx *database.Tx) (struct{}, error) {
		current, err := p.lock(ctx, tx)
		if err != nil {
			return struct{}{}, err
		}
		if current != p.intent || current.State != Pending {
			return struct{}{}, ErrChanged
		}
		return struct{}{}, audit.At(p.now()).RecordIn(ctx, tx, "apptown-system", p.source.ProjectID, "landing.merge-unknown", fmt.Sprintf("run %d", p.source.RunID), struct {
			Operation string `json:"operation"`
			Expected  string `json:"expected"`
			Note      string `json:"note"`
		}{current.Operation, current.Expected, reason})
	})
	return err
}
func (p *preparedMerge) record() Record {
	return Record{p.intent.State, p.intent.Operation, p.intent.Expected, p.intent.Result}
}

package scm

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

var (
	ErrClosurePending  = errors.New("the merge's outcome is not yet recorded, so this change cannot be closed yet; once the merge is inspected, deploy or fix forward if it landed, or close the change if it did not")
	ErrClosureRecorded = errors.New("this change is already on main; deploy it or fix forward, it cannot be abandoned")
	// errMergeFinal: a landing whose merge is recorded cannot merge again.
	errMergeFinal = errors.New("this landing already merged; its merge is final — fix forward in a new change")
)

// Closure serializes a decision to stop work against repository effects. Acquire
// it before opening a transaction and release it after commit or rollback. Work
// supplies all relevant run IDs, including completed history, under its row locks.
// The provider path has no durable intent before its remote call; an unrecorded
// provider outcome is not covered by this local-merge guarantee.
type Closure struct {
	project ids.ID
	release func()
	closed  atomic.Bool
}

func PrepareClosure(ctx context.Context, locks *RepositoryLocks, project ids.ID) (*Closure, error) {
	release, err := locks.Acquire(ctx, project)
	if err != nil {
		return nil, err
	}
	return &Closure{project: project, release: release}, nil
}

func (c *Closure) Close() {
	if c != nil && !c.closed.Swap(true) {
		c.release()
	}
}

// CheckIn never acquires a repository lock inside the caller's transaction.
func (c *Closure) CheckIn(ctx context.Context, tx *database.Tx, project ids.ID, runs []ids.ID) error {
	if c == nil || c.closed.Load() || c.project != project {
		return fmt.Errorf("work closure requires its live repository scope")
	}
	for _, id := range runs {
		intent, err := readIntent(ctx, tx, id)
		if err != nil {
			return err
		}
		switch intent.State {
		case Pending:
			return ErrClosurePending
		case Recorded:
			return ErrClosureRecorded
		}
	}
	return nil
}

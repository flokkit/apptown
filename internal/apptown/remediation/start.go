package remediation

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type startRecords interface {
	Start(context.Context, Item, StartDefinition, string) (*workflow.Run, error)
	Failed(context.Context, Item, string) error
}

// This owner resolves a frozen definition, commits the complete attempt, then
// requests execution. Failed dispatch leaves the same run recoverable.
type starter struct {
	records  startRecords
	resolve  func(context.Context, Item) (StartDefinition, error)
	dispatch func(runID ids.ID) error
}

func NewStarter(records startRecords, resolve func(context.Context, Item) (StartDefinition, error), dispatch func(ids.ID) error) *starter {
	return &starter{records: records, resolve: resolve, dispatch: dispatch}
}

func (s *starter) Start(ctx context.Context, item Item, worker string) (*workflow.Run, error) {
	def, err := s.resolve(ctx, item)
	if err != nil {
		// A resolution the caller stopped was never assessed: the item stays
		// queued for the next pass, never stuck on the stop.
		if ctx.Err() != nil {
			return nil, err
		}
		if recordErr := s.records.Failed(ctx, item, err.Error()); recordErr != nil {
			return nil, fmt.Errorf("%v; recording failed start: %w", err, recordErr)
		}
		return nil, err
	}
	run, err := s.records.Start(ctx, item, def, worker)
	if err != nil || run == nil {
		return run, err
	}
	if err := s.dispatch(run.ID); err != nil {
		return run, fmt.Errorf("attempt committed as run %d; dispatch needs recovery: %w", run.ID, err)
	}
	return run, nil
}

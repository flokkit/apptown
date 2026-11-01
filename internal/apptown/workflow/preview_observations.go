package workflow

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/ids"
)

// previewSubject is local work identity for the background preview collector.
// It grants no access to a person's preview read or boot.
type previewSubject struct {
	ID, ProjectID ids.ID
	InProgress    bool
}

func (s *Service) PreviewObservationSubjects(ctx context.Context, listed ...ids.ID) ([]previewSubject, error) {
	rows, err := workflowsql.New(s.db.SQL()).PreviewObservationChanges(ctx, workflowsql.PreviewObservationChangesParams{OpenState: ChangeInProgress.String(), Listed: listed})
	if err != nil {
		return nil, err
	}
	out := make([]previewSubject, 0, len(rows))
	for _, r := range rows {
		out = append(out, previewSubject{ID: r.ID, ProjectID: r.ProjectID, InProgress: r.State == ChangeInProgress.String()})
	}
	return out, nil
}

// PreviewObservationSubject selects local facts for a queued check, without
// granting a person's read or preview permission.
func (s *Service) PreviewObservationSubject(ctx context.Context, id ids.ID) (previewSubject, error) {
	r, err := workflowsql.New(s.db.SQL()).PreviewObservationChange(ctx, id)
	return previewSubject{ID: r.ID, ProjectID: r.ProjectID, InProgress: r.State == ChangeInProgress.String()}, err
}

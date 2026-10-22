package workflow

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// Changes supplies checked Change operations that need no dispatcher. Preview
// reads and adoption uplift can be constructed before run executors. Service
// uses the same policy and transaction implementation.
type Changes struct {
	db        *database.DB
	authority operationAuthority
	clocked
}

// NewChanges is change commands outside Work's runs; now is AppTown's
// clock (nil the real one).
func NewChanges(db *database.DB, authority access.Authorizer, now func() time.Time) Changes {
	return Changes{db: db, authority: commandAuthority{db: db, checked: authority, clocked: clocked{now}}, clocked: clocked{now}}
}
func (s Changes) scope(ctx context.Context, id ids.ID, act commandAction) (ids.ID, commandActor, error) {
	if _, err := requireActor(ctx, s.authority, operationPermission{Action: actionIdentify}); err != nil {
		return ids.Nil, commandActor{}, err
	}
	project, err := workflowsql.New(s.db.SQL()).ChangeProjectMeta(ctx, id)
	if err != nil {
		return ids.Nil, commandActor{}, database.Missing(err)
	}
	who, err := requireActor(ctx, s.authority, operationPermission{Action: act, ProjectID: project, ChangeID: id})
	return project, who, err
}

// ProjectHasAny says whether any Change was ever opened on a project, in any
// state. It is a fact, not a permission: callers authorize their own act.
func (s Changes) ProjectHasAny(ctx context.Context, projectID ids.ID) (bool, error) {
	return workflowsql.New(s.db.SQL()).ProjectHasChange(ctx, projectID)
}

func (s Changes) Change(ctx context.Context, id ids.ID) (*Change, error) {
	project, _, err := s.scope(ctx, id, actionReadChange)
	if err != nil {
		return nil, err
	}
	ch, err := readChangeInProject(ctx, s.db, id, project)
	if err != nil {
		return nil, err
	}
	if ch.ProjectID != project {
		return nil, &access.Denied{Reason: "Work scope changed during read"}
	}
	return ch, nil
}

func (s Changes) Uplift(ctx context.Context, in Uplift) (*Change, error) {
	who, err := requireActor(ctx, s.authority, operationPermission{Action: actionOpenUplift, ProjectID: in.ProjectID})
	if err != nil {
		return nil, err
	}
	return (changeStore{s.db, s.clocked}).openUplift(ctx, in, who.Receipt)
}

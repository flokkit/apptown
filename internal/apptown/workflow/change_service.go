package workflow

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
)

func (s *Service) changeScope(ctx context.Context, id ids.ID, act commandAction) (ids.ID, commandActor, error) {
	if s == nil {
		return ids.Nil, commandActor{}, ErrCallerRequired
	}
	return (Changes{db: s.db, authority: s.authority, clocked: s.clocked}).scope(ctx, id, act)
}
func (s *Service) Change(ctx context.Context, id ids.ID) (*Change, error) {
	if s == nil {
		return nil, ErrCallerRequired
	}
	return (Changes{db: s.db, authority: s.authority, clocked: s.clocked}).Change(ctx, id)
}
func (s *Service) Changes(ctx context.Context, project ids.ID, in ChangeList) ([]ChangeSummary, error) {
	if project.IsZero() {
		return nil, fmt.Errorf("Change lists require a project")
	}
	if _, err := s.require(ctx, operationPermission{Action: actionListChanges, ProjectID: project}); err != nil {
		return nil, err
	}
	return readChangeList(ctx, s.db, project, in)
}
func (s *Service) Create(ctx context.Context, in ChangeDraft) (*Change, error) {
	return s.CreateWith(ctx, in, nil)
}

// CreateWith admits a Change with join run inside its admission transaction
// (see AdmissionParticipant). A join error leaves no Change; a workspace
// preparation failure after commit returns the recorded Change with the error.
func (s *Service) CreateWith(ctx context.Context, in ChangeDraft, join AdmissionParticipant) (*Change, error) {
	who, err := s.require(ctx, operationPermission{Action: actionCreateChange, ProjectID: in.ProjectID})
	if err != nil {
		return nil, err
	}
	return s.creator.CreateWith(ctx, in, who, join)
}
func (s *Service) Edit(ctx context.Context, in ChangeEdit) (*Change, error) {
	project, who, err := s.changeScope(ctx, in.ID, actionEditBrief)
	if err != nil {
		return nil, err
	}
	if !in.ProjectID.IsZero() && in.ProjectID != project {
		return nil, &access.Denied{Reason: "Change does not belong to the requested project"}
	}
	in.ProjectID = project
	return (changeStore{s.db, s.clocked}).edit(ctx, in, who.Receipt)
}
func (s *Service) Import(ctx context.Context, in ImportBranch) (*Change, error) {
	who, err := s.require(ctx, operationPermission{Action: actionImportChange, ProjectID: in.ProjectID})
	if err != nil {
		return nil, err
	}
	return s.importer.Import(ctx, in, who)
}
func (s *Service) Uplift(ctx context.Context, in Uplift) (*Change, error) {
	if s == nil {
		return nil, ErrCallerRequired
	}
	return (Changes{db: s.db, authority: s.authority, clocked: s.clocked}).Uplift(ctx, in)
}

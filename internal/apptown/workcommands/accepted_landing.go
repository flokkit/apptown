package workcommands

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

func (a AcceptedLandings) Advance(ctx context.Context, workID ids.ID) error {
	_, err := a.Work.AdvanceAcceptedLanding(ctx, workID, a)
	return err
}

// AcceptedLandings composes current lane availability with Workflow's guarded handoff.
type AcceptedLandings struct {
	DB    *database.DB
	Work  *workflow.Service
	Lanes *projectlifecycle.Service
}

func (a AcceptedLandings) CanLand(ctx context.Context, projectID ids.ID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	record, projectErr := project.ByIDIn(ctx, a.DB, projectID)
	if projectErr != nil {
		return false, fmt.Errorf("project %d is unavailable", projectID)
	}
	_, err := a.Lanes.SelectedWorkflow(ctx, &record, "land")
	return err == nil, nil
}

// LandingSentences reads what a project's landing approvals would say if its
// landing started now, from the landing its way of working pins.
type LandingSentences struct {
	DB    *database.DB
	Lanes *projectlifecycle.Service
}

func (l LandingSentences) LandingMeans(ctx context.Context, projectID ids.ID) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, err := project.ByIDIn(ctx, l.DB, projectID)
	if err != nil {
		return nil, err
	}
	landing, err := l.Lanes.SelectedWorkflow(ctx, &record, "land")
	if err != nil {
		return nil, err
	}
	var means []string
	for _, stage := range landing.Doc.Spec.Stages {
		for _, step := range append(append([]schema.Step{}, stage.OnEnter...), stage.Steps...) {
			if m, _ := step.With["means"].(string); step.Uses == "approval" && m != "" {
				means = append(means, m)
			}
		}
	}
	return means, nil
}

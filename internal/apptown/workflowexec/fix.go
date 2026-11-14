package workflowexec

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/fix"
)

const FixKind = "fix"
const RoutineKind = "routine"

type fixConfig struct {
	Kind string `json:"kind"`
	For  string `json:"for"`
}
type fixDeclarations interface {
	Fixes(context.Context, adoption.Source) ([]fix.Declaration, error)
}

func (a *commands) declarations(ctx context.Context, run *workflow.Run) ([]fix.Declaration, error) {
	if a.fixes == nil {
		return nil, fmt.Errorf("fix declarations are unavailable")
	}
	revision, err := adoption.ByIDIn(ctx, a.db, run.AdoptionID)
	if err != nil {
		return nil, err
	}
	return a.fixes.Fixes(ctx, adoption.Source{ProjectID: run.ProjectID, Revision: revision})
}

func runTargets(run *workflow.Run) ([]check.Finding, error) {
	return fix.Targets(run.InputsJSON)
}

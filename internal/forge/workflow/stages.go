package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// Scanner contains only the facts needed to expand a declaration. Credentials
// and execution configuration stay with the scanner owner.
type Scanner struct{ Name, Target string }
type ScannerSource interface {
	SnapshotIn(context.Context, database.Queryer) ([]Scanner, error)
}
type Stages struct {
	scanners  ScannerSource
	automatic func(string) bool
}

func NewStages(scanners ScannerSource, automatic func(string) bool) Stages {
	return Stages{scanners, automatic}
}

type ReadyStep struct {
	ID      ids.ID
	Type    string
	OnEnter bool
	Name    string
}

// EnterIn joins the caller's project-first admission/advancement transaction.
// The entire declaration is prepared before its receipt or any step is written.
func (w Stages) EnterIn(ctx context.Context, tx *database.Tx, runID ids.ID, spec *schema.Spec, name string, at time.Time) ([]ReadyStep, error) {
	if spec == nil || runID.IsZero() || name == "" || w.automatic == nil {
		return nil, fmt.Errorf("stage admission needs a run, definition and execution registry")
	}
	st, _ := spec.Stage(name)
	if st == nil {
		return nil, fmt.Errorf("stage %q not in definition", name)
	}
	var scanners []Scanner
	if w.scanners != nil {
		for _, step := range append(append([]schema.Step{}, st.OnEnter...), st.Steps...) {
			if scanTarget(step) != "" {
				var err error
				scanners, err = w.scanners.SnapshotIn(ctx, tx)
				if err != nil {
					return nil, err
				}
				break
			}
		}
	}
	plan, err := planStage(st, scanners)
	if err != nil {
		return nil, err
	}
	checks := make([]Check, 0, len(plan))
	for _, p := range plan {
		checks = append(checks, p.check)
	}
	body, err := json.Marshal(stageEnteredReceipt{Stage: name, Checks: checks})
	if err != nil {
		return nil, err
	}
	if err := recordEventIn(ctx, tx, runID, "stage.entered", body, at); err != nil {
		return nil, err
	}
	var auto []ReadyStep
	for _, p := range plan {
		row, err := workflowsql.New(tx.SQL()).InsertStageStep(ctx, workflowsql.InsertStageStepParams{ID: ids.New(at), RunID: runID, Stage: name, Name: p.check.Name, Type: p.check.Type, ConfigJson: json.RawMessage(p.configJSON), Status: StatusPending.String()})
		if err != nil {
			return nil, err
		}
		if w.automatic != nil && w.automatic(p.check.Type) {
			auto = append(auto, ReadyStep{ID: row.ID, Type: p.check.Type, OnEnter: p.onEnter, Name: p.check.Name})
		}
	}
	return auto, nil
}

type stageEnteredReceipt struct {
	Stage  string  `json:"stage"`
	Checks []Check `json:"checks"`
}

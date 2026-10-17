package workflow

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// StartSource is the Work-owned admission observation used by an external
// definition resolver. Superseded landings are explicit candidates for the
// existing abandonment participant; creation still rechecks under project lock.
type StartSource struct {
	Change     *Change
	AdoptionID ids.ID
	Superseded []RunRef
}

func (s runAdmissions) Source(ctx context.Context, in StartRequest) (StartSource, error) {
	var out StartSource
	if in.ProjectID.IsZero() {
		return out, fmt.Errorf("invalid Run source scope")
	}
	tx, err := s.records.db.ReadSnapshot(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if !in.ChangeID.IsZero() {
		out.Change, err = readChangeInProject(ctx, tx, in.ChangeID, in.ProjectID)
		if err != nil {
			return out, err
		}
	}
	if in.ParentRunID.IsZero() && out.Change != nil {
		if !out.Change.State.CanRun() {
			return out, fmt.Errorf("change #%d is %s — only in-progress changes take runs", out.Change.ID, out.Change.State)
		}
		flights, err := workflowsql.New(tx.SQL()).ActiveChangeFlightsMeta(ctx, workflowsql.ActiveChangeFlightsMetaParams{ProjectID: in.ProjectID, ChangeID: in.ChangeID})
		if err != nil {
			return out, database.Missing(err)
		}
		for _, run := range flights {
			if in.AcceptedRunID.IsZero() && run.Role == "landing" && run.Active == 0 && run.Failed > 0 {
				out.Superseded = append(out.Superseded, RunRef{ID: run.ID, ProjectID: in.ProjectID})
			} else {
				return out, fmt.Errorf("change #%d already has a run in flight (run %d)", in.ChangeID, run.ID)
			}
		}
	}
	out.AdoptionID, err = startAdoption(ctx, tx, in)
	if err != nil {
		return out, err
	}
	if !in.ParentRunID.IsZero() {
		parent, err := readScopedRun(ctx, tx, in.ParentRunID, in.ProjectID)
		if err != nil {
			return out, err
		}
		if parent.ChangeID != in.ChangeID {
			return out, fmt.Errorf("parent run belongs to another change")
		}
		source, err := ReadSourceIn(ctx, tx, RunRef{ID: in.ParentRunID, ProjectID: in.ProjectID})
		if err != nil {
			return out, err
		}
		if source.Depth >= MaxCallDepth {
			return out, fmt.Errorf("call depth cap (%d) reached — check for workflow cycles", MaxCallDepth)
		}
	}
	if err := tx.Commit(); err != nil {
		return StartSource{}, err
	}
	return out, nil
}

// startAdoption selects the Work lineage's pin; the adoption domain decodes the
// receipt. No parent ever substitutes today's project adoption for a missing pin.
func startAdoption(ctx context.Context, db database.Queryer, in StartRequest) (ids.ID, error) {
	var id ids.ID
	switch {
	case !in.ParentRunID.IsZero():
		var err error
		id, err = workflowsql.New(db.SQL()).ParentRunAdoptionMeta(ctx, workflowsql.ParentRunAdoptionMetaParams{ID: in.ParentRunID, ProjectID: in.ProjectID})
		if err != nil {
			return ids.Nil, database.Missing(err)
		}
		if id.IsZero() {
			return ids.Nil, fmt.Errorf("parent run %d has no recorded adoption", in.ParentRunID)
		}
		return id, nil
	case !in.ChangeID.IsZero():
		var err error
		id, err = workflowsql.New(db.SQL()).ChangeTargetAdoptionMeta(ctx, workflowsql.ChangeTargetAdoptionMetaParams{ID: in.ChangeID, ProjectID: in.ProjectID})
		if err != nil {
			return ids.Nil, database.Missing(err)
		}
	}
	if id.IsZero() {
		var err error
		id, err = adoption.CurrentReceiptIDIn(ctx, db, in.ProjectID)
		if err != nil {
			return ids.Nil, err
		}
	}
	if id.IsZero() {
		return ids.Nil, fmt.Errorf("project %d has no recorded adoption", in.ProjectID)
	}
	return id, nil
}

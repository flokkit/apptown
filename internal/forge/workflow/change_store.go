package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/stamp"
)

func readChange(ctx context.Context, db database.Queryer, id ids.ID) (*Change, error) {
	return readChangeInProject(ctx, db, id, ids.Nil)
}

func readChangeInProject(ctx context.Context, db database.Queryer, id ids.ID, project ids.ID) (*Change, error) {
	q := workflowsql.New(db.SQL())
	ch, err := changeRecord(q.ChangeByID(ctx, workflowsql.ChangeByIDParams{ID: id, ProjectID: project}))
	if err == nil && ch.State == ChangeAbandoned {
		var receipts []audit.Entry
		receipts, err = audit.Recent(ctx, db, audit.Filter{ProjectID: ch.ProjectID, KindPrefix: "change.abandoned", Subject: fmt.Sprintf("change %d", ch.ID), Limit: 1})
		if err == nil && len(receipts) > 0 && receipts[0].ProjectID == ch.ProjectID && receipts[0].Kind == "change.abandoned" {
			var receipt abandonAudit
			if err = json.Unmarshal(receipts[0].Payload, &receipt); err == nil {
				ch.AbandonReason = receipt.Reason
			}
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missingChange{id}
	}
	return ch, err
}

func changeRecord(row workflowsql.Change, err error) (*Change, error) {
	if err != nil {
		return nil, err
	}
	state, err := ParseChangeState(row.State)
	if err != nil {
		return nil, err
	}
	return &Change{ID: row.ID, Number: row.Number, ProjectID: row.ProjectID, Type: row.Type, Title: row.Title, Body: row.Body,
		RunID: row.RunID, State: state, Branch: row.Branch, BaseSHA: row.BaseSha, LandedSHA: row.LandedSha,
		ExternalRef: row.ExternalRef, Summary: row.Summary, Provenance: row.Provenance,
		CreatedAt: row.CreatedAt.Format(time.RFC3339Nano), ClosedAt: stamp.Time{NullTime: row.ClosedAt}, ClosedAs: row.ClosedAs,
		TargetAdoptionID: row.TargetAdoptionID, AcceptanceJSON: string(row.AcceptanceJson),
		ExpectedRevision: row.ExpectedRevision, AdoptedRevision: row.AdoptedRevision, TargetCandidateJSON: string(row.TargetCandidateJson),
		EditRevision: row.EditRevision, OpenedBy: row.OpenedBy, OpenedByPrincipal: row.OpenedByPrincipal,
		RequestMet: ParseRequestMet(row.RequestMet), RequestGap: row.RequestGap}, nil
}

func ReadChangeIn(ctx context.Context, db database.Queryer, id ids.ID) (*Change, error) {
	return readChange(ctx, db, id)
}

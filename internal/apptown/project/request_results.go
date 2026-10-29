package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
	"time"
)

var ErrRequestReused = fault.New(fault.Conflict, "this request was already used for different project input")
var ErrRequestRequired = fault.New(fault.Invalid, "send a requestId for this project operation")

// CreationClaim exists in the caller's transaction. Fresh must be completed
// before commit; otherwise the caller must return an error and roll back.
type CreationClaim struct {
	ID, ProjectID            ids.ID
	Fresh                    bool
	InputHash, ResolvedGroup string
	Result                   Project
}

func ClaimCreationIn(ctx context.Context, tx *database.Tx, at time.Time, actor, request ids.ID) (CreationClaim, error) {
	var out CreationClaim
	if actor.IsZero() || request.IsZero() {
		return out, ErrRequestRequired
	}
	id, pid := ids.New(at.UTC()), ids.New(at.UTC())
	n, err := queries(tx).ClaimProjectCreation(ctx, projectsql.ClaimProjectCreationParams{ID: id, ActorID: actor, RequestID: request, ProjectID: pid})
	if err != nil {
		return out, err
	}
	if n == 1 {
		return CreationClaim{ID: id, ProjectID: pid, Fresh: true}, nil
	}
	row, err := queries(tx).ProjectCreationRequest(ctx, projectsql.ProjectCreationRequestParams{ActorID: actor, RequestID: request})
	if err != nil {
		return out, err
	}
	if !row.InputHash.Valid || !row.ResolvedGroup.Valid || row.ResultJson == nil {
		return out, fmt.Errorf("project creation request has no committed result")
	}
	result, err := requestResult(*row.ResultJson, row.ProjectID)
	return CreationClaim{ID: row.ID, ProjectID: row.ProjectID, InputHash: row.InputHash.String, ResolvedGroup: row.ResolvedGroup.String, Result: result}, err
}
func CompleteCreationIn(ctx context.Context, tx *database.Tx, claim CreationClaim, hash, group string) error {
	raw, err := snapshotResult(ctx, tx, claim.ProjectID)
	if err != nil {
		return err
	}
	n, err := queries(tx).CompleteProjectCreation(ctx, projectsql.CompleteProjectCreationParams{ID: claim.ID, InputHash: sql.NullString{String: hash, Valid: true}, ResolvedGroup: sql.NullString{String: group, Valid: true}, ResultJson: &raw})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("project creation result was not recorded")
	}
	return nil
}

func snapshotResult(ctx context.Context, tx *database.Tx, id ids.ID) (json.RawMessage, error) {
	row, err := queries(tx).ProjectByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(row)
}
func requestResult(raw json.RawMessage, id ids.ID) (Project, error) {
	var row projectsql.Project
	if err := json.Unmarshal(raw, &row); err != nil {
		return Project{}, fmt.Errorf("read project request result: %w", err)
	}
	if row.ID != id || !ValidSlug(row.Slug) {
		return Project{}, fmt.Errorf("project request result has invalid identity")
	}
	return record(row), nil
}

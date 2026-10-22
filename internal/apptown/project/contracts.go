package project

import (
	"context"
	"database/sql"
	"github.com/flokkit/apptown/internal/ids"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project/internal/projectsql"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
)

// Contracts are the snapshots of a project's native declaration at a
// commit, kept as evidence: launches read their selected tree; snapshots
// never modify or supply that source.
type Contracts struct {
	db    *database.DB
	clock func() time.Time
}

// Snapshotted reports whether the contract at a commit was already recorded.
func (c *Contracts) Snapshotted(ctx context.Context, projectID ids.ID, sha string) (bool, error) {
	return queries(c.db).ContractSnapshotExists(ctx, projectsql.ContractSnapshotExistsParams{ProjectID: projectID, Sha: sha})
}

// Snapshot records the contract at a commit: present with its bytes,
// absent, or invalid with the error; a repeat replaces the record.
func (c *Contracts) Snapshot(ctx context.Context, projectID ids.ID, sha, status, format, appYAML, errMsg string) error {
	return queries(c.db).SaveContractSnapshot(ctx, projectsql.SaveContractSnapshotParams{ProjectID: projectID, Sha: sha, Status: status, AppYaml: sql.NullString{String: appYAML, Valid: true}, SourceFormat: format, Error: sql.NullString{String: errMsg, Valid: true}, CreatedAt: clock.Or(c.clock)().UTC()})
}

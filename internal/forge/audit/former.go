package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/audit/internal/auditsql"
	"flokkit.io/forge/internal/ids"
)

// The kinds of former identity a link made before 1.0 can name: the tables
// the identity cutover converted, whose pages and API paths took the old
// integer.
const (
	FormerChange        = "changes"
	FormerRun           = "runs"
	FormerStep          = "steps"
	FormerADR           = "adrs"
	FormerGuidanceBlock = "guidance_blocks"
)

// FormerID is the record that a former identity of this forge (forge) names:
// the id the identity cutover gave the row of kind whose identity was old.
// A former identity the forge did not keep is not found, in words.
func FormerID(ctx context.Context, q database.Queryer, forge ids.ID, kind, old string) (ids.ID, error) {
	id, err := auditsql.New(q.SQL()).FormerID(ctx, auditsql.FormerIDParams{Forge: forge, Kind: kind, Old: old})
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, fault.New(fault.NotFound, fmt.Sprintf("no record had the identity %s before 1.0", old))
	}
	if err != nil {
		return ids.Nil, database.QueryError(err, "FormerID")
	}
	return id, nil
}

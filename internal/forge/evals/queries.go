package evals

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/evals/internal/evalssql"
)

func queries(q database.Queryer) *evalssql.Queries { return evalssql.New(q.SQL()) }

// LiveEvalsFromIn names the live evals with an arm still working from the
// project's code: a project they copy from is not deleted under them.
func (s *Service) LiveEvalsFromIn(ctx context.Context, tx *database.Tx, projectID ids.ID) ([]ids.ID, error) {
	return queries(tx).LiveEvalsFromProject(ctx, projectID)
}

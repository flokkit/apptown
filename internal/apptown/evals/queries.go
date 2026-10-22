package evals

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/evals/internal/evalssql"
	"github.com/flokkit/apptown/internal/database"
)

func queries(q database.Queryer) *evalssql.Queries { return evalssql.New(q.SQL()) }

// LiveEvalsFromIn names the live evals with an arm still working from the
// project's code: a project they copy from is not deleted under them.
func (s *Service) LiveEvalsFromIn(ctx context.Context, tx *database.Tx, projectID ids.ID) ([]ids.ID, error) {
	return queries(tx).LiveEvalsFromProject(ctx, projectID)
}

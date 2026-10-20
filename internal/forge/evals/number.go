package evals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/evals/internal/evalssql"
	"flokkit.io/forge/internal/ids"
)

// IDByNumber is the id of the eval people name by its number within AppTown
// ("eval 9"). It says only that the number is taken; reading the record still
// asks for authority.
func (s *Service) IDByNumber(ctx context.Context, number int64) (ids.ID, error) {
	id, err := evalssql.New(s.db.SQL()).EvalIDByNumber(ctx, number)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, fault.New(fault.NotFound, fmt.Sprintf("no eval %d", number))
	}
	return id, err
}

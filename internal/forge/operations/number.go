package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/operations/internal/operationssql"
	"flokkit.io/forge/internal/ids"
)

// IDByNumber is the id of the operation people name by its number within the forge
// ("operation 9"). It says only that the number is taken; reading the record still
// asks for authority.
func (s *Service) IDByNumber(ctx context.Context, number int64) (ids.ID, error) {
	id, err := operationssql.New(s.db.SQL()).OperationIDByNumber(ctx, number)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, fault.New(fault.NotFound, fmt.Sprintf("no operation %d", number))
	}
	return id, err
}

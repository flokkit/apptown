package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/operations/internal/operationssql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// IDByNumber is the id of the operation people name by its number within AppTown
// ("operation 9"). It says only that the number is taken; reading the record still
// asks for authority.
func (s *Service) IDByNumber(ctx context.Context, number int64) (ids.ID, error) {
	id, err := operationssql.New(s.db.SQL()).OperationIDByNumber(ctx, number)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, fault.New(fault.NotFound, fmt.Sprintf("no operation %d", number))
	}
	return id, err
}

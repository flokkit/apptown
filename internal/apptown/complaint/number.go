package complaint

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/complaint/internal/complaintsql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// IDByNumber is the id of the complaint people name by its number within AppTown
// ("complaint 9"). It says only that the number is taken; reading the record still
// asks for authority.
func (b *Box) IDByNumber(ctx context.Context, number int64) (ids.ID, error) {
	id, err := complaintsql.New(b.db.SQL()).ComplaintIDByNumber(ctx, number)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, fault.New(fault.NotFound, fmt.Sprintf("no complaint %d", number))
	}
	return id, err
}

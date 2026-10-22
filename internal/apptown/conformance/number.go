package conformance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/conformance/internal/conformancesql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// ReviewIDByNumber is the id of the conformance review people name by its number within AppTown
// ("conformance review 9"). It says only that the number is taken; reading the record still
// asks for authority.
func (s *Service) ReviewIDByNumber(ctx context.Context, number int64) (ids.ID, error) {
	id, err := conformancesql.New(s.db.SQL()).ReviewIDByNumber(ctx, number)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, fault.New(fault.NotFound, fmt.Sprintf("no conformance review %d", number))
	}
	return id, err
}

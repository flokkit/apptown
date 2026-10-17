package conformance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/conformance/internal/conformancesql"
	"flokkit.io/forge/internal/ids"
)

// ReviewIDByNumber is the id of the conformance review people name by its number within the forge
// ("conformance review 9"). It says only that the number is taken; reading the record still
// asks for authority.
func (s *Service) ReviewIDByNumber(ctx context.Context, number int64) (ids.ID, error) {
	id, err := conformancesql.New(s.db.SQL()).ReviewIDByNumber(ctx, number)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, fault.New(fault.NotFound, fmt.Sprintf("no conformance review %d", number))
	}
	return id, err
}

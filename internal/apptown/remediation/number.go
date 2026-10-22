package remediation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/remediation/internal/remediationsql"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// IDByNumber is the id of the remediation people name by its number within AppTown
// ("remediation 9"). It says only that the number is taken; reading the record still
// asks for authority.
func (s *Service) IDByNumber(ctx context.Context, number int64) (ids.ID, error) {
	id, err := remediationsql.New(s.db.SQL()).RemediationIDByNumber(ctx, number)
	if errors.Is(err, sql.ErrNoRows) {
		return ids.Nil, fault.New(fault.NotFound, fmt.Sprintf("no remediation %d", number))
	}
	return id, err
}

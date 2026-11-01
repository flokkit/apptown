package identity

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/access"

	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
)

// access.RootGrant identifies the single authority that can administer the instance.
// Callers supply the principal's currently active grants, including memberships.
// Grants are judged live as of at, the caller's clock.
func rootIn(ctx context.Context, q database.Queryer, username string, at time.Time) (bool, error) {
	return PrincipalRootIn(ctx, q, access.Principal{Kind: "user", Name: username}, at)
}

// PrincipalRootIn is RootIn for a principal: by its ID when it has one.
func PrincipalRootIn(ctx context.Context, q database.Queryer, pr access.Principal, at time.Time) (bool, error) {
	grants, err := ActiveGrantsIn(ctx, q, pr, at)
	_, root := access.RootGrant(grants)
	return root, err
}

func accountIn(ctx context.Context, q database.Queryer, row accesssql.User, at time.Time) (Account, error) {
	a := account(row)
	root, err := rootIn(ctx, q, row.Username, at)
	if root {
		a.Role = "admin"
	}
	return a, err
}

// Identity and credential administration are root-equivalent; a bare grant
// cannot create a second, partial source of that authority.
func preserveRoot(ctx context.Context, tx *database.Tx, at time.Time, apply func() error) error {
	if err := LockUsersIn(ctx, tx); err != nil {
		return err
	}
	before, err := enabledRoots(ctx, tx, at)
	if err != nil {
		return err
	}
	if err := apply(); err != nil {
		return err
	}
	if before > 0 {
		after, err := enabledRoots(ctx, tx, at)
		if err != nil {
			return err
		}
		if after == 0 {
			return errLastAdministrator
		}
	}
	enabled, err := queries(tx).ExternalProviderEnabled(ctx)
	if err != nil {
		return err
	}
	if enabled {
		n, err := queries(tx).PersistentLocalRoots(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return errLocalRecovery
		}
	}
	return nil
}
func enabledRoots(ctx context.Context, q database.Queryer, at time.Time) (int, error) {
	rows, err := queries(q).AdminUsers(ctx, at)
	n := 0
	for _, row := range rows {
		if !row.DisabledAt.Valid {
			n++
		}
	}
	return n, err
}

func completeSetupIn(ctx context.Context, tx *database.Tx) error {
	_, err := queries(tx).PutInstanceSetting(ctx, accesssql.PutInstanceSettingParams{Key: "setup_completed", Value: "true"})
	return err
}

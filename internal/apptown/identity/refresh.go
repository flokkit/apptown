package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/stamp"
)

// Refresh rotates a presented credential and records the outcome atomically.
// A leaked credential ends the delegation before ErrLeaked reaches the caller.
func (s *Service) Refresh(ctx context.Context, refresh string) (Pair, error) {
	return s.RefreshForClient(ctx, refresh, "")
}

// RefreshForClient additionally checks an optional OAuth client binding.
func (s *Service) RefreshForClient(ctx context.Context, refresh, clientID string) (Pair, error) {
	return s.oauth.refresh(ctx, refresh, clientID)
}

func (o *oauthStore) refresh(ctx context.Context, presented, clientID string) (Pair, error) {
	// A refresh token from before the AppTown rename (forge_rt_…) belongs to
	// a delegation whose stored scopes (forge:…) no longer grant anything
	// but reads. It is refused like an unknown one, so the client authorises
	// again rather than renewing into a connection whose writes fail.
	if !strings.HasPrefix(presented, RefreshPrefix) {
		return Pair{}, ErrUnknownRefresh
	}
	var pair Pair
	var outcome error
	now := o.now().UTC()
	err := o.db.InTx(ctx, func(tx *database.Tx) error {
		q := queries(tx)
		// Discover identity without locking a token before its delegation.
		observed, err := q.TokenByHashAndKind(ctx, accesssql.TokenByHashAndKindParams{TokenHash: HashToken(presented), Kind: "refresh"})
		found, err := database.Lookup(err)
		if err != nil {
			return err
		}
		if !found {
			return ErrUnknownRefresh
		}
		row, err := q.DelegationByIDForUpdate(ctx, observed.DelegationID)
		found, err = database.Lookup(err)
		if err != nil {
			return err
		}
		if !found {
			return ErrConnectionEnded
		}
		token, err := q.RefreshTokenForUpdate(ctx, observed.TokenHash)
		found, err = database.Lookup(err)
		if err != nil {
			return err
		}
		if !found || token.DelegationID != row.ID {
			return ErrUnknownRefresh
		}
		d := connection(accesssql.DelegationsNamed(row))
		d.refresh = refreshCredential{hash: token.TokenHash, expires: token.ExpiresAt, revoked: stamp.Time{NullTime: token.RevokedAt}}
		// As before, reuse is evidence of a leak even if an optional client
		// identifier is wrong or the connection has since expired.
		if !token.RevokedAt.Valid && (!d.LiveAt(now) || !token.ExpiresAt.After(now)) {
			return ErrConnectionEnded
		}
		if !token.RevokedAt.Valid && clientID != "" && clientID != d.ClientID {
			return ErrClientMismatch
		}
		pair, outcome = d.Rotate(presented, now)
		if errors.Is(outcome, ErrLeaked) {
			d.end(now)
			if err := saveDelegationIn(ctx, tx, accesssql.DelegationsNamed(row), d); err != nil {
				return err
			}
			if err := revokeDelegationTokensIn(ctx, tx, d.ID, now); err != nil {
				return err
			}
			return audit.At(now).RecordIn(ctx, tx, d.User, ids.Nil, "oauth.refresh-leaked", d.ClientName, map[string]any{"delegationId": d.ID})
		}
		if outcome != nil {
			return outcome
		}
		n, err := q.RevokeRefreshToken(ctx, accesssql.RevokeRefreshTokenParams{TokenHash: token.TokenHash, Kind: token.Kind,
			DelegationID: token.DelegationID, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt, RevokedAt: d.refresh.revoked.NullTime})
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("refresh consumption was not recorded")
		}
		if n, err := q.InsertTokenPair(ctx, accesssql.InsertTokenPairParams{DelegationID: d.ID, At: now,
			AccessHash: HashToken(pair.Access), AccessExpires: now.Add(AccessTTL), RefreshHash: HashToken(pair.Refresh), RefreshExpires: now.Add(RefreshTTL)}); err != nil {
			return err
		} else if n != 2 {
			return errors.New("token pair was not recorded")
		}
		return audit.At(now).RecordIn(ctx, tx, d.User, ids.Nil, "oauth.refreshed", d.ClientName, map[string]any{"delegationId": d.ID})
	})
	if err != nil {
		return Pair{}, err
	}
	if outcome != nil {
		return Pair{}, outcome
	}
	return pair, nil
}

// The locked row supplies immutable stored fields, including bounds not shown
// in the connected-agents view. The object owns the transition being saved.
func saveDelegationIn(ctx context.Context, tx *database.Tx, row accesssql.DelegationsNamed, d Delegation) error {
	_, err := queries(tx).SaveDelegation(ctx, accesssql.SaveDelegationParams{ID: row.ID,
		ClientID: row.ClientID, ClientName: row.ClientName, Audience: row.Audience, Kind: row.Kind, ScopesJson: row.ScopesJson,
		GroupBoundsJson: row.GroupBoundsJson, ProjectBoundsJson: row.ProjectBoundsJson, AuthTime: row.AuthTime, AuthMethod: row.AuthMethod,
		CreatedAt: row.CreatedAt, LastUsed: row.LastUsed, ExpiresAt: row.ExpiresAt, RevokedAt: d.RevokedAt.NullTime})
	return err
}

func revokeDelegationTokensIn(ctx context.Context, tx *database.Tx, id ids.ID, at time.Time) error {
	changed, err := queries(tx).RevokeDelegationTokens(ctx, accesssql.RevokeDelegationTokensParams{DelegationID: id, At: at})
	if err != nil {
		return err
	}
	if changed.Selected != changed.Removed {
		return errors.New("delegation tokens were not all revoked")
	}
	return nil
}

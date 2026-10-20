package identity

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/access"
	"testing"
)

// A refresh token from before the AppTown rename is refused like an unknown
// one: its delegation's stored scopes no longer grant writes, so the client
// must authorise again instead of renewing into a connection whose consented
// writes fail (review of the AppTown rename, R3).
//
// Kills: the refresh path accepting a token without the current prefix.
func TestAPreRenameRefreshTokenIsRefused(t *testing.T) {
	t.Parallel()
	s, db, id, pair := connectedService(t)
	ctx := context.Background()
	oldRefresh := "forge_rt_review_pre_rename"
	// sql-guarantee: reproduce the stored delegation and credential written by rc.5.
	if _, err := db.Exec(`UPDATE delegations SET scopes_json = '["forge:change:create"]'::jsonb WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: rc.5 issued forge_rt_ refresh tokens; no owner operation of this build can issue one.
	if _, err := db.Exec(`UPDATE oauth_tokens SET token_hash = ? WHERE token_hash = ?`, HashToken(oldRefresh), HashToken(pair.Refresh)); err != nil {
		t.Fatal(err)
	}
	d, err := s.oauth.Connection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.RefreshForClient(ctx, oldRefresh, d.ClientID)
	if !errors.Is(err, ErrUnknownRefresh) {
		t.Fatalf("a pre-rename refresh token renewed into scopes %v (covering change-create: %v): %v", next.Scopes, access.ScopesCover(next.Scopes, access.CapChangeCreate), err)
	}
}

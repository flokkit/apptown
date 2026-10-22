package identity

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
)

func TestOAuthResolutionUsesCurrentBounds(t *testing.T) {
	t.Parallel()
	s, _, _, pair := connectedService(t)
	testAccounts(t, s.db, "alice", "boss")
	ctx := context.Background()
	got, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser)
	if err != nil || got.Principal.Name != "alice" || got.Principal.Role != "member" || got.ClientID == "" || got.Principal.DelegationID.IsZero() {
		t.Fatalf("identity: %+v %v", got, err)
	}
	got.Principal.Scopes[0] = "altered"
	again, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser)
	if err != nil || again.Principal.Scopes[0] != "apptown:user:read" {
		t.Fatalf("scope alias: %+v %v", again, err)
	}
	if err := testSaveAccount(s.db, accountEdit{Name: "alice", Role: "admin"}, "setup"); err != nil {
		t.Fatal(err)
	}
	got, err = s.ResolveOAuth(ctx, pair.Access, AudienceUser)
	if err != nil || got.Principal.Role != "admin" {
		t.Fatalf("live role: %+v %v", got, err)
	}
	// Keep another administrator so normal account rules allow demotion.
	if err := s.SeedAdministrator(ctx, "boss"); err != nil {
		t.Fatal(err)
	}
	if err := testSaveAccount(s.db, accountEdit{Name: "alice", Role: "member"}, "setup"); err != nil {
		t.Fatal(err)
	}
	got, err = s.ResolveOAuth(ctx, pair.Access, AudienceUser)
	if err != nil || got.Principal.Role != "member" {
		t.Fatalf("demotion: %+v %v", got, err)
	}
	disabled := true
	if err := testSaveAccount(s.db, accountEdit{Name: "alice", Role: "member", Disabled: &disabled}, "setup"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser); !errors.Is(err, ErrOAuthCredential) {
		t.Fatalf("disabled: %v", err)
	}
}
func TestOAuthResolutionRejectsInvalidCredentials(t *testing.T) {
	t.Parallel()
	for _, which := range []string{"prefix", "unknown", "audience", "expired-token", "revoked-token", "expired-delegation", "revoked-delegation"} {
		t.Run(which, func(t *testing.T) {
			t.Parallel()
			s, db, id, pair := connectedService(t)
			token, audience := pair.Access, AudienceUser
			var err error
			switch which {
			case "prefix":
				token = "other"
			case "unknown":
				token = AccessPrefix + "unknown"
			case "audience":
				audience = "/mcp/admin"
			case "expired-token":
				// sql-guarantee: Expire only the existing access token while its delegation remains live; RevokeRefreshToken cannot update access tokens and InsertTokenPair cannot replace their hashes.
				_, err = db.Exec(`UPDATE oauth_tokens SET expires_at=$1 WHERE token_hash=$2`, time.Now().Add(-time.Minute), HashToken(token))
			case "revoked-token":
				_, err = queries(db).RevokeDelegationTokens(context.Background(), accesssql.RevokeDelegationTokensParams{DelegationID: id, At: time.Now()})
			case "expired-delegation":
				record, readErr := queries(db).DelegationByID(context.Background(), id)
				if readErr != nil {
					t.Fatal(readErr)
				}
				record.ExpiresAt = sql.NullTime{Time: time.Now().Add(-time.Minute), Valid: true}
				saveStoredDelegation(t, db, record)
			case "revoked-delegation":
				err = s.Disconnect(context.Background(), id, "setup")
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.ResolveOAuth(context.Background(), token, audience)
			if !errors.Is(err, ErrOAuthCredential) || got.Principal.Name != "" {
				t.Fatalf("invalid credential: %+v %v", got, err)
			}
		})
	}
}
func TestOAuthResolutionFailureSemantics(t *testing.T) {
	t.Parallel()
	s, db, _, pair := connectedService(t)
	testAccounts(t, db, "alice")
	ctx := context.Background()
	for _, marker := range []string{"-- name: TokenByHashAndKind", "-- name: DelegationByID :one", "-- name: TouchUser"} {
		restore := db.FailNextStatement(marker)
		got, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser)
		restore()
		if err == nil || errors.Is(err, ErrOAuthCredential) || got.Principal.Name != "" {
			t.Fatalf("read failure: %+v %v", got, err)
		}
	}
	// Last-use metadata is best effort; account facts above are required.
	for _, marker := range []string{"-- name: TouchDelegation"} {
		restore := db.FailNextStatement(marker)
		got, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser)
		restore()
		if err != nil || got.Principal.Name != "alice" || got.Principal.Role != "member" {
			t.Fatalf("last-use stamp: %+v %v", got, err)
		}
	}
}
func TestOAuthTokenRevocationSettlement(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"-- name: SaveDelegation", "-- name: RevokeDelegationTokens", "INSERT INTO audit_log"} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			s, db, _, pair := connectedService(t)
			ctx := context.Background()
			if err := s.RevokeOAuthToken(ctx, "unknown"); err != nil {
				t.Fatal(err)
			}
			restore := db.FailNextStatement(marker)
			err := s.RevokeOAuthToken(ctx, pair.Refresh)
			restore()
			if err == nil {
				t.Fatal("revocation write failure hidden")
			}
			if _, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser); err != nil {
				t.Fatalf("partial revocation: %v", err)
			}
			if receipts := ledger(t, db, "oauth.disconnected"); len(receipts) != 0 {
				t.Fatalf("partial receipt: %+v", receipts)
			}
			if err := s.RevokeOAuthToken(ctx, pair.Refresh); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if _, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser); !errors.Is(err, ErrOAuthCredential) {
				t.Fatalf("revoked token admitted: %v", err)
			}
		})
	}
}

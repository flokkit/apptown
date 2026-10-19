package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/ids/idstest"
)

// RequireStill resolves the credential an operation captured, not the
// person's name: of two equivalent tokens, revoking the one that asked
// refuses the next effect although the person's grants are unchanged,
// while the other still stands. A session that logged out, an OAuth
// connection withdrawn or narrowed, a disabled account, a lost grant and a
// missing or unknown reference all refuse.
//
// Kills: checking only the principal's grants; resolving any live token of
// the person; widening an OAuth connection back to what was captured.
func TestRequireStillResolvesTheCredentialThatAsked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	read := access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ID: idstest.N(7).String(), ProjectID: idstest.N(7)}, Context: access.Context{Route: "test.effect", Consequential: true}}
	setup := func(t *testing.T) *Service {
		t.Helper()
		s := admissionService(t)
		if _, err := s.Grant(ctx, Spec{SubjectType: "user", Subject: "alice", Capability: string(access.CapProjectRead), Scope: access.ScopeInstance, By: "setup"}, ""); err != nil {
			t.Fatal(err)
		}
		return s
	}
	tokenPrincipal := func(t *testing.T, s *Service, name string) access.Principal {
		t.Helper()
		plain, err := testIssueToken(s, name, "alice", "member", "setup")
		if err != nil {
			t.Fatal(err)
		}
		pr, err := s.PersonalTokenIdentity(ctx, plain, nil)
		if err != nil || pr.Credential != TokenCredential(name) {
			t.Fatalf("token %s: %+v %v", name, pr, err)
		}
		return pr
	}

	t.Run("tokens", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		asked, other := tokenPrincipal(t, s, "laptop"), tokenPrincipal(t, s, "desktop")
		if err := s.RequireStill(ctx, asked, read); err != nil {
			t.Fatalf("a live token: %v", err)
		}
		if err := s.SeedAdministrator(ctx, "boss"); err != nil {
			t.Fatal(err)
		}
		if revoked, err := s.RevokeToken(ctx, "laptop", testPrincipal("boss")); err != nil || !revoked {
			t.Fatalf("revoking: %v %v", revoked, err)
		}
		if err := s.RequireStill(ctx, asked, read); !errors.Is(err, ErrCredentialGone) {
			t.Fatalf("the revoked token that asked: %v", err)
		}
		if err := s.RequireStill(ctx, other, read); err != nil {
			t.Fatalf("an equivalent token that was not revoked: %v", err)
		}
		forged := other
		forged.Purpose = access.TokenAgent
		if err := s.RequireStill(ctx, forged, read); !errors.Is(err, ErrCredentialGone) {
			t.Fatalf("a token whose purpose differs from what asked: %v", err)
		}
		if err := s.RequireStill(ctx, other, access.Permission{Capability: access.CapDeploymentTeardown, Resource: read.Resource, Context: read.Context}); err == nil {
			t.Fatal("a capability the grants never held was allowed")
		}
	})

	t.Run("session", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		cookie, err := issueSessionIn(ctx, s.db, "alice", s.sessions.ttl, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		pr, err := s.SessionIdentity(ctx, cookie)
		if err != nil || pr.Credential == "" {
			t.Fatalf("session: %+v %v", pr, err)
		}
		if err := s.RequireStill(ctx, pr, read); err != nil {
			t.Fatalf("a live session: %v", err)
		}
		// An expired session that was not yet removed on sight still refuses.
		// sql-guarantee: no operation expires a session early; the clock passing its expiry is the state.
		if _, err := s.db.Exec(`UPDATE sessions SET expires_at = now() - interval '1 minute' WHERE principal_id = ?`, principalOf(t, s.db, "alice")); err != nil {
			t.Fatal(err)
		}
		if err := s.RequireStill(ctx, pr, read); !errors.Is(err, ErrCredentialGone) {
			t.Fatalf("an expired session: %v", err)
		}
		if err := s.sessions.End(ctx, cookie); err != nil {
			t.Fatal(err)
		}
		if err := s.RequireStill(ctx, pr, read); !errors.Is(err, ErrCredentialGone) {
			t.Fatalf("a session after logout: %v", err)
		}
	})

	t.Run("oauth", func(t *testing.T) {
		t.Parallel()
		s, db, id, pair := connectedService(t)
		if _, err := s.Grant(ctx, Spec{SubjectType: "user", Subject: "alice", Capability: string(access.CapProjectRead), Scope: access.ScopeInstance, By: "setup"}, ""); err != nil {
			t.Fatal(err)
		}
		resolved, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser)
		if err != nil || resolved.Principal.Credential != DelegationCredential(id) {
			t.Fatalf("oauth: %+v %v", resolved, err)
		}
		pr := resolved.Principal
		if err := s.RequireStill(ctx, pr, read); err != nil {
			t.Fatalf("a live connection: %v", err)
		}
		row, err := queries(db).DelegationByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		scopes := row.ScopesJson
		row.ScopesJson = []byte(`[]`)
		saveStoredDelegation(t, db, row)
		if err := s.RequireStill(ctx, pr, read); err == nil {
			t.Fatal("a connection narrowed since it asked was allowed what it no longer holds")
		}
		row.ScopesJson = scopes
		saveStoredDelegation(t, db, row)
		if err := s.RequireStill(ctx, pr, read); err != nil {
			t.Fatalf("the connection with its scopes again: %v", err)
		}
		if err := s.Disconnect(ctx, id, "alice"); err != nil {
			t.Fatal(err)
		}
		if err := s.RequireStill(ctx, pr, read); !errors.Is(err, ErrCredentialGone) {
			t.Fatalf("a withdrawn connection: %v", err)
		}
	})

	t.Run("references", func(t *testing.T) {
		t.Parallel()
		s := setup(t)
		for _, pr := range []access.Principal{
			{Kind: "user", Name: "alice", Via: "token"},
			{Kind: "user", Name: "alice", Credential: "token:nothing-of-that-name"},
			{Kind: "user", Name: "alice", Credential: "session:999"},
			{Kind: "user", Name: "alice", Credential: "console:abc"},
			{Kind: "user", Name: "alice", Credential: HeaderCredential("bob")},
			{Kind: "user", Name: "alice", Credential: "system:scheduled-copy"},
		} {
			if err := s.RequireStill(ctx, pr, read); err == nil {
				t.Errorf("%q was allowed", pr.Credential)
			}
		}
		if err := s.RequireStill(ctx, access.Principal{Kind: "user", Name: "alice", Via: "proxy", Credential: HeaderCredential("alice")}, read); err != nil {
			t.Fatalf("a proxy-attributed request whose account stands: %v", err)
		}
		disabled := true
		if err := testSaveAccount(s.db, accountEdit{Name: "alice", Role: "member", Disabled: &disabled}, "setup"); err != nil {
			t.Fatal(err)
		}
		if err := s.RequireStill(ctx, access.Principal{Kind: "user", Name: "alice", Via: "proxy", Credential: HeaderCredential("alice")}, read); !errors.Is(err, ErrCredentialGone) {
			t.Fatalf("a disabled account: %v", err)
		}
		if err := s.RequireStill(ctx, access.Principal{Kind: "instrument", Name: "forge-system", Instrument: "forge", Credential: "system:scheduled-copy"}, read); err != nil {
			t.Fatalf("AppTown's own binding: %v", err)
		}
	})
}

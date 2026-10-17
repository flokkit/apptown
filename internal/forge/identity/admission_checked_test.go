package identity

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
)

func TestAdmissionRequiresReadableAccount(t *testing.T) {
	t.Parallel()
	s := admissionService(t)
	ctx := context.Background()
	session, err := issueSessionIn(context.Background(), s.db, "alice", s.sessions.ttl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := testIssueToken(s, "cli", "alice", "member", "setup")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		resolve func() (access.Principal, error)
	}{
		{"session", func() (access.Principal, error) { return s.SessionIdentity(ctx, session) }},
		{"token", func() (access.Principal, error) { return s.PersonalTokenIdentity(ctx, token, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := s.db.FailNextStatement("-- name: TouchUser")
			pr, err := tc.resolve()
			restore()
			if err == nil || pr.Name != "" {
				t.Fatalf("unreadable account admitted %q: %v", pr.Name, err)
			}
		})
	}
	in := exchangeFixture(t, s)
	pair, err := s.ExchangeCode(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	restore := s.db.FailNextStatement("-- name: TouchUser")
	resolved, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser)
	restore()
	if err == nil || resolved.Principal.Name != "" {
		t.Fatalf("OAuth admitted unreadable account %q: %v", resolved.Principal.Name, err)
	}
	restore = s.db.FailNextStatement("-- name: UserByName")
	signed, err := s.SignIn(ctx, PasswordLogin{Username: "alice", Password: "admission-password"})
	restore()
	if err == nil || signed.Token != "" {
		t.Fatalf("password read failure: %v", err)
	}
}
func TestDisabledAccountCannotSignInOrUseSession(t *testing.T) {
	t.Parallel()
	s := admissionService(t)
	ctx := context.Background()
	disabled := true
	if err := testSaveAccount(s.db, accountEdit{Name: "alice", Role: "member", Disabled: &disabled}, "setup"); err != nil {
		t.Fatal(err)
	}
	// A retained or racing session must still check the current account.
	session, err := issueSessionIn(context.Background(), s.db, "alice", s.sessions.ttl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if pr, err := s.SessionIdentity(ctx, session); err == nil || pr.Name != "" {
		t.Errorf("disabled session admitted %q: %v", pr.Name, err)
	}
	if signed, err := s.SignIn(ctx, PasswordLogin{Username: "alice", Password: "admission-password"}); err == nil || signed.Token != "" {
		t.Errorf("disabled password admitted: %v", err)
	}
}
func TestCredentialLastUseRemainsBestEffort(t *testing.T) {
	t.Parallel()
	s := admissionService(t)
	ctx := context.Background()
	session, err := issueSessionIn(context.Background(), s.db, "alice", s.sessions.ttl, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := testIssueToken(s, "cli", "alice", "member", "setup")
	if err != nil {
		t.Fatal(err)
	}
	restore := s.db.FailNextStatement("-- name: TouchSession")
	pr, err := s.SessionIdentity(ctx, session)
	restore()
	if err != nil || pr.Name != "alice" {
		t.Fatalf("session stamp became authority: %+v %v", pr, err)
	}
	restore = s.db.FailNextStatement("-- name: TouchAuthToken")
	pr, err = s.PersonalTokenIdentity(ctx, token, nil)
	restore()
	if err != nil || pr.Name != "alice" {
		t.Fatalf("token stamp became authority: %+v %v", pr, err)
	}
}

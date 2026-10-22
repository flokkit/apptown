package identity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func authorizationFixture(t *testing.T, s *Service) BrowserAuthorization {
	t.Helper()
	testAccounts(t, s.db, "alice")
	c, err := s.RegisterOAuthClient(context.Background(), Registration{Name: "agent", RedirectURIs: []string{"http://localhost:1/cb"}})
	if err != nil {
		t.Fatal(err)
	}
	return BrowserAuthorization{User: "alice", Binding: "browser", Request: AuthorizationRequest{ClientID: c.ID, RedirectURI: c.RedirectURIs[0], Challenge: "challenge", Method: "S256"}}
}

func TestAuthorizationCannotBypassValidation(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	ctx := context.Background()
	for _, redirects := range [][]string{nil, {"http://example.com/callback"}, {"missing-scheme"}} {
		if _, err := s.RegisterOAuthClient(ctx, Registration{RedirectURIs: redirects}); err == nil {
			t.Fatal("invalid registration accepted")
		}
	}
	b := authorizationFixture(t, s)
	for _, tc := range []struct {
		name   string
		change func(*AuthorizationRequest)
		safe   bool
	}{
		{"unknown client", func(r *AuthorizationRequest) { r.ClientID = "unknown" }, false},
		{"redirect", func(r *AuthorizationRequest) { r.RedirectURI = "https://attacker.example/cb" }, false},
		{"method", func(r *AuthorizationRequest) { r.Method = "plain" }, true},
		{"challenge", func(r *AuthorizationRequest) { r.Challenge = "" }, true},
		{"scope", func(r *AuthorizationRequest) { r.Scope = "unknown" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := b
			tc.change(&changed.Request)
			_, err := s.PrepareConsent(ctx, changed)
			var problem *AuthorizationProblem
			if !errors.As(err, &problem) || problem.SafeRedirect != tc.safe {
				t.Fatalf("rejection: %v", err)
			}
			if _, err := s.DecideConsent(ctx, ConsentDecision{Browser: changed, Choice: ConsentAllow}); !errors.As(err, &problem) {
				t.Fatalf("decision bypass: %v", err)
			}
		})
	}
	view, err := s.InspectAuthorization(ctx, b.Request)
	if err != nil || len(view.Scopes) != 1 || view.Scopes[0] != "apptown:user:read" {
		t.Fatalf("default scope: %+v %v", view, err)
	}
}

func TestConsentOperationBindingAndClaim(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "bob")
	s := New(db, Config{})
	ctx := context.Background()
	b := authorizationFixture(t, s)
	prepared, err := s.PrepareConsent(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ConsentDecision){
		func(d *ConsentDecision) { d.Browser.User = "bob" },
		func(d *ConsentDecision) { d.Browser.Binding = "other" },
		func(d *ConsentDecision) { d.Browser.Request.State = "changed" },
		func(d *ConsentDecision) { d.Browser.Request.Resource = "changed" },
		func(d *ConsentDecision) { d.Token = "missing" },
	} {
		d := ConsentDecision{Browser: b, Token: prepared.Token, Choice: ConsentAllow}
		change(&d)
		if _, err := s.DecideConsent(ctx, d); err == nil {
			t.Fatal("altered consent accepted")
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.DecideConsent(ctx, ConsentDecision{Browser: b, Token: prepared.Token, Choice: ConsentAllow})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			var failure *ConsentError
			if !errors.As(err, &failure) || failure.Failure != ConsentChanged {
				t.Fatal(err)
			}
		}
	}
	if successes != 1 {
		t.Fatalf("consent successes: %d", successes)
	}
	prepared, err = s.PrepareConsent(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: Expire the prepared consent before its decision; InsertConsent cannot replace the existing token and ConsumeConsent deletes it, bypassing the expiry check.
	if _, err := db.Exec(`UPDATE oauth_consents SET expires_at=$1 WHERE token_hash=$2`, time.Now().Add(-time.Minute), HashToken(prepared.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecideConsent(ctx, ConsentDecision{Browser: b, Token: prepared.Token, Choice: ConsentAllow}); err == nil {
		t.Fatal("expired consent accepted")
	}
}

func TestConsentFailureKeepsExistingSettlement(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Config{})
	ctx := context.Background()
	b := authorizationFixture(t, s)
	prepared, err := s.PrepareConsent(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	restore := db.FailNextStatement("INSERT INTO oauth_codes")
	_, err = s.DecideConsent(ctx, ConsentDecision{Browser: b, Token: prepared.Token, Choice: ConsentAllow})
	restore()
	var failure *ConsentError
	if !errors.As(err, &failure) || failure.Failure != ConsentIssueFailed {
		t.Fatalf("issue failure: %v", err)
	}
	// Consent was already claimed: this is deliberately not a transaction rewrite.
	_, err = s.DecideConsent(ctx, ConsentDecision{Browser: b, Token: prepared.Token, Choice: ConsentAllow})
	if !errors.As(err, &failure) || failure.Failure != ConsentChanged {
		t.Fatalf("claimed consent: %v", err)
	}
	prepared, err = s.PrepareConsent(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DecideConsent(ctx, ConsentDecision{Browser: b, Token: prepared.Token, Choice: ConsentDeny})
	if err != nil || result.Choice != ConsentDeny || result.Code != "" {
		t.Fatalf("deny: %+v %v", result, err)
	}
	var n int
	// sql-guarantee: A denial hands back no code, so no owner read could find one; only the table total proves none was minted.
	if err := db.QueryRow(`SELECT count(*) FROM oauth_codes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("denial minted a code: %d %v", n, err)
	}
}

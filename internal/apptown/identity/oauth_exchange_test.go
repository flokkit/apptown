package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func exchangeFixture(t *testing.T, s *Service) CodeExchange {
	t.Helper()
	ctx := context.Background()
	b := authorizationFixture(t, s)
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	b.Request.Challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	p, err := s.PrepareConsent(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DecideConsent(ctx, ConsentDecision{Browser: b, Token: p.Token, Choice: ConsentAllow})
	if err != nil {
		t.Fatal(err)
	}
	return CodeExchange{Code: result.Code, Verifier: verifier, ClientID: b.Request.ClientID, RedirectURI: b.Request.RedirectURI, AuthMethod: "internal"}
}
func TestCodeExchangeValidation(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Config{})
	ctx := context.Background()
	in := exchangeFixture(t, s)
	for _, change := range []func(*CodeExchange){
		func(in *CodeExchange) { in.Code = "unknown" }, func(in *CodeExchange) { in.Verifier = "wrong" }, func(in *CodeExchange) { in.ClientID = "other" }, func(in *CodeExchange) { in.RedirectURI = "https://other.example/cb" },
	} {
		changed := in
		change(&changed)
		_, err := s.ExchangeCode(ctx, changed)
		var problem *CodeExchangeProblem
		if !errors.As(err, &problem) {
			t.Fatalf("invalid grant: %v", err)
		}
	}
	// sql-guarantee: Expire the same issued authorization code after other validation probes; InsertCode cannot replace it and ConsumeCode changes used_at instead of expiry.
	if _, err := db.SQL().ExecContext(context.Background(), `UPDATE oauth_codes SET expires_at=$1 WHERE code_hash=$2`, time.Now().Add(-time.Minute), HashToken(in.Code)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExchangeCode(ctx, in); err == nil {
		t.Fatal("expired code accepted")
	}
	in = exchangeFixture(t, s)
	pair, err := s.ExchangeCode(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExchangeCode(ctx, in); !errors.Is(err, ErrCodeReplay) {
		t.Fatalf("replay: %v", err)
	}
	token, err := s.oauth.Token(context.Background(), pair.Access, "access")
	if err != nil || token == nil || !token.Revoked {
		t.Fatalf("replay did not revoke: %+v %v", token, err)
	}
}
func TestCodeExchangeSettlement(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"-- name: ConsumeCode", "-- name: InsertDelegation", "-- name: TouchClient", "INSERT INTO audit_log", "-- name: InsertTokenPair"} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			db := testDB(t)
			s := New(db, Config{})
			in := exchangeFixture(t, s)
			ctx := context.Background()
			restore := db.FailNextStatement(marker)
			pair, err := s.ExchangeCode(ctx, in)
			restore()
			if err == nil || pair.Access != "" || pair.Refresh != "" {
				t.Fatalf("partial result: %+v %v", pair, err)
			}
			var n int
			// sql-guarantee: A failed exchange hands back nothing to look up, so only table totals prove no delegation, token or receipt survived the rollback.
			if err := db.SQL().QueryRowContext(context.Background(), `SELECT (SELECT count(*) FROM delegations)+(SELECT count(*) FROM oauth_tokens)+(SELECT count(*) FROM audit_log WHERE kind='oauth.connected')`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("partial settlement: %d %v", n, err)
			}
			if _, err := s.ExchangeCode(ctx, in); err != nil {
				t.Fatalf("retry: %v", err)
			}
		})
	}
}
func TestCodeExchangeConcurrentClaim(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	in := exchangeFixture(t, s)
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.ExchangeCode(context.Background(), in); results <- err }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrCodeReplay) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("exchange successes: %d", successes)
	}
}
func TestCodeExchangeCancellationDoesNotSpendCode(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	in := exchangeFixture(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ExchangeCode(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled exchange: %v", err)
	}
	if _, err := s.ExchangeCode(context.Background(), in); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

// A losing request can have read an unused code before the winning request
// commits. Force that ordering without sleeps or scheduler-dependent races.
func TestCodeReplayAfterStaleReadRevokesWinningTokens(t *testing.T) {
	t.Parallel()
	s := New(testDB(t), Config{})
	ctx := context.Background()
	in := exchangeFixture(t, s)
	stale, err := s.oauth.CodeByValue(ctx, in.Code)
	if err != nil {
		t.Fatal(err)
	}
	winner, err := s.ExchangeCode(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the winner's two database timestamps equal, but explicitly in the
	// past. The database server and Go process may have different clocks.
	consumed := time.Now().Add(-time.Minute)
	// sql-guarantee: Backdate the consumed code to the exact winning delegation creation time for stale-read replay detection; ConsumeCode only stamps database now() on unused codes.
	if _, err := s.db.SQL().ExecContext(context.Background(), `UPDATE oauth_codes SET used_at=$1 WHERE code_hash=$2`, consumed, HashToken(in.Code)); err != nil {
		t.Fatal(err)
	}
	winningToken, err := queries(s.db).TokenByHash(ctx, HashToken(winner.Access))
	if err != nil {
		t.Fatal(err)
	}
	delegation, err := queries(s.db).DelegationByID(ctx, winningToken.DelegationID)
	if err != nil {
		t.Fatal(err)
	}
	delegation.CreatedAt = consumed
	saveStoredDelegation(t, s.db, delegation)
	if _, err := s.exchangeCode(ctx, in, stale); !errors.Is(err, ErrCodeReplay) {
		t.Fatalf("stale exchange: %v", err)
	}
	for _, entry := range []struct{ value, kind string }{{winner.Access, "access"}, {winner.Refresh, "refresh"}} {
		token, err := s.oauth.Token(context.Background(), entry.value, entry.kind)
		if err != nil || token == nil || !token.Revoked {
			t.Fatalf("winning %s credential survived replay: %+v %v", entry.kind, token, err)
		}
	}
}

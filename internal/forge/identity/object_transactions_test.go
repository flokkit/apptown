package identity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

func connectedService(t *testing.T) (*Service, *database.DB, ids.ID, Pair) {
	t.Helper()
	db := testDB(t)
	s := New(db, Config{})
	in := exchangeFixture(t, s)
	pair, err := s.ExchangeCode(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	id, found, err := s.oauth.DelegationOfToken(context.Background(), pair.Access)
	if err != nil || !found {
		t.Fatalf("connection: %d %v %v", id, found, err)
	}
	return s, db, id, pair
}

func TestRefreshTransactionRollsBackFailedWrites(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"-- name: RevokeRefreshToken", "-- name: InsertTokenPair", "INSERT INTO audit_log"} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			s, db, _, pair := connectedService(t)
			restore := db.FailNextStatement(marker)
			failed, err := s.Refresh(context.Background(), pair.Refresh)
			restore()
			if err == nil || failed.Access != "" || failed.Refresh != "" {
				t.Fatalf("failed rotation returned a pair: %+v %v", failed, err)
			}
			token, err := s.oauth.Token(context.Background(), pair.Refresh, "refresh")
			if err != nil || token == nil || token.Revoked {
				t.Fatalf("failed rotation spent the credential: %+v %v", token, err)
			}
			var n int
			// sql-guarantee: A failed rotation hands back no pair, so only the table total proves no new token row survived.
			if err := db.QueryRow(`SELECT count(*) FROM oauth_tokens`).Scan(&n); err != nil || n != 2 {
				t.Fatalf("failed rotation left minted tokens: %d %v", n, err)
			}
			if _, err := s.Refresh(context.Background(), pair.Refresh); err != nil {
				t.Fatalf("explicit retry: %v", err)
			}
		})
	}
}

func TestRefreshLeakReceiptCommitsOrRollsBackWithRevocation(t *testing.T) {
	t.Parallel()
	s, db, id, pair := connectedService(t)
	next, err := s.Refresh(context.Background(), pair.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	restore := db.FailNextStatement("INSERT INTO audit_log")
	_, err = s.Refresh(context.Background(), pair.Refresh)
	restore()
	if err == nil || errors.Is(err, ErrLeaked) {
		t.Fatalf("failed revocation reported a committed leak: %v", err)
	}
	d, err := s.Connection(context.Background(), id)
	if err != nil || !d.LiveAt(time.Now()) {
		t.Fatalf("failed receipt ended delegation: %+v %v", d, err)
	}
	at, err := s.oauth.Token(context.Background(), next.Access, "access")
	if err != nil || at == nil || at.Revoked {
		t.Fatal("failed receipt revoked tokens")
	}
	if _, err := s.Refresh(context.Background(), pair.Refresh); !errors.Is(err, ErrLeaked) {
		t.Fatalf("reuse: %v", err)
	}
	d, err = s.Connection(context.Background(), id)
	if err != nil || d.LiveAt(time.Now()) {
		t.Fatalf("ErrLeaked returned before revocation committed: %+v %v", d, err)
	}
	at, err = s.oauth.Token(context.Background(), next.Access, "access")
	if err != nil || at == nil || !at.Revoked {
		t.Fatal("leak left winner's token live")
	}
	if receipts := ledger(t, db, "oauth.refresh-leaked"); len(receipts) != 1 {
		t.Fatalf("leak receipts: %+v", receipts)
	}
}

func TestRefreshChecksIdentityBinding(t *testing.T) {
	t.Parallel()
	s, _, _, pair := connectedService(t)
	if _, err := s.RefreshForClient(context.Background(), pair.Refresh, "another-client"); !errors.Is(err, ErrClientMismatch) {
		t.Fatalf("client mismatch: %v", err)
	}
	if _, err := s.Refresh(context.Background(), "missing"); !errors.Is(err, ErrUnknownRefresh) {
		t.Fatalf("unknown token: %v", err)
	}
	if _, err := s.Refresh(context.Background(), pair.Refresh); err != nil {
		t.Fatalf("binding refusal consumed the token: %v", err)
	}
}

func TestRefreshRejectsPartialTokenWrites(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"mint", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			s, db, id, first := connectedService(t)
			pair := first
			if operation == "revoke" {
				var err error
				pair, err = s.Refresh(context.Background(), first.Refresh)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`CREATE FUNCTION veto_access_token() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN IF NEW.kind = 'access' THEN RETURN NULL; END IF; RETURN NEW; END $$`); err != nil {
				t.Fatal(err)
			}
			statement := `CREATE TRIGGER veto_token BEFORE INSERT ON oauth_tokens FOR EACH ROW EXECUTE FUNCTION veto_access_token()`
			if operation == "revoke" {
				statement = `CREATE TRIGGER veto_token BEFORE UPDATE ON oauth_tokens FOR EACH ROW EXECUTE FUNCTION veto_access_token()`
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			if got, err := s.Refresh(context.Background(), first.Refresh); err == nil || errors.Is(err, ErrLeaked) || got.Access != "" {
				t.Fatalf("partial %s succeeded: %+v %v", operation, got, err)
			}
			d, err := s.Connection(context.Background(), id)
			if err != nil || !d.LiveAt(time.Now()) {
				t.Fatal("partial write ended delegation")
			}
			at, err := s.oauth.Token(context.Background(), pair.Access, "access")
			if err != nil || at == nil || at.Revoked {
				t.Fatal("partial write altered existing token")
			}
			if _, err := db.Exec(`DROP TRIGGER veto_token ON oauth_tokens`); err != nil {
				t.Fatal(err)
			}
			_, err = s.Refresh(context.Background(), first.Refresh)
			if operation == "mint" && err != nil {
				t.Fatal(err)
			}
			if operation == "revoke" && !errors.Is(err, ErrLeaked) {
				t.Fatalf("retry leak: %v", err)
			}
		})
	}
}

func TestRefreshCancellationDoesNotSpendCredential(t *testing.T) {
	t.Parallel()
	s, _, _, pair := connectedService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Refresh(ctx, pair.Refresh); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled refresh: %v", err)
	}
	if _, err := s.Refresh(context.Background(), pair.Refresh); err != nil {
		t.Fatalf("cancelled operation spent token: %v", err)
	}
}

func TestTicketReceiptFailureAndConcurrentConsumption(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice")
	s := New(db, Config{})
	_, ticket, err := s.IssueTicket(context.Background(), "alice", ids.Nil, idstest.N(40), "refs/heads/apptown/change-40", "abc")
	if err != nil {
		t.Fatal(err)
	}
	restore := db.FailNextStatement("INSERT INTO audit_log")
	consumed, err := s.ConsumeTicket(context.Background(), ticket.ID)
	restore()
	if err == nil || consumed {
		t.Fatalf("missing receipt consumed ticket: %v %v", consumed, err)
	}
	start := make(chan struct{})
	results := make(chan bool, 5)
	failures := make(chan error, 5)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok, err := s.ConsumeTicket(context.Background(), ticket.ID)
			results <- ok
			failures <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(failures)
	winners := 0
	for ok := range results {
		if ok {
			winners++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("consumption winners: %d", winners)
	}
	if receipts := ledger(t, db, "git-ticket.consumed"); len(receipts) != 1 {
		t.Fatalf("consumption receipts: %+v", receipts)
	}
}

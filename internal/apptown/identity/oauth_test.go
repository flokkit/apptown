package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/identity/internal/accesssql"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A client registers itself; a person's consent is a code used once; the
// exchange mints a delegation and its first pair in one transaction; a
// replay mints nothing and revokes what the first exchange minted.
func TestConsentAndExchange(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice")
	s := New(db, Config{})
	o := s.oauth
	ctx := context.Background()
	c, err := s.RegisterOAuthClient(ctx, Registration{Name: "  ", RedirectURIs: []string{"http://localhost:1/cb"}})
	if err != nil || !strings.HasPrefix(c.ID, ClientPrefix) || c.Name != "a coding agent" {
		t.Fatalf("register: %v %+v", err, c)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	b := BrowserAuthorization{User: "alice", Binding: "browser", Request: AuthorizationRequest{ClientID: c.ID, RedirectURI: c.RedirectURIs[0], Scope: "apptown:user:read apptown:change:create", Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Method: "S256"}}
	prepared, err := s.PrepareConsent(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DecideConsent(ctx, ConsentDecision{Browser: b, Token: prepared.Token, Choice: ConsentAllow})
	if err != nil {
		t.Fatal(err)
	}
	in := CodeExchange{Code: result.Code, Verifier: verifier, ClientID: c.ID, RedirectURI: b.Request.RedirectURI, AuthMethod: "internal"}
	pair, err := s.ExchangeCode(ctx, in)
	if err != nil || !strings.HasPrefix(pair.Access, AccessPrefix) || !strings.HasPrefix(pair.Refresh, RefreshPrefix) || len(pair.Scopes) != 2 {
		t.Fatalf("exchange: %v %+v", err, pair)
	}
	did, found, err := o.DelegationOfToken(context.Background(), pair.Access)
	if err != nil || !found {
		t.Fatalf("delegation: %v %v", found, err)
	}
	if _, err := s.ExchangeCode(ctx, in); !errors.Is(err, ErrCodeReplay) {
		t.Fatalf("a second exchange is a replay: %v", err)
	}
	if tok, err := o.Token(context.Background(), pair.Access, "access"); err != nil || tok == nil || !tok.Revoked {
		t.Fatalf("a replay revokes what the code minted: %+v %v", tok, err)
	}

	conn, err := o.Connection(context.Background(), did)
	if err != nil || conn == nil || conn.User != "alice" || conn.ClientName != c.Name || len(conn.Scopes) != 2 || !conn.LiveAt(time.Now()) {
		t.Fatalf("connection: %v %+v", err, conn)
	}
	var names []string
	for _, receipt := range ledger(t, db, "") {
		if strings.HasPrefix(receipt.Kind, "oauth.") {
			names = append(names, receipt.Kind)
		}
	}
	sort.Strings(names)
	if kinds := strings.Join(names, ","); kinds != "oauth.authorized,oauth.client-registered,oauth.connected" {
		t.Fatalf("receipts: %v", kinds)
	}
}

// Refresh tokens rotate once; a rotated token presented again is a leak
// that ends the delegation; a revoke ends everything in one transaction.
func TestRotationLeaksAndRevocation(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice")
	s := New(db, Config{})
	o := s.oauth
	c, _ := o.RegisterClient(context.Background(), "agent", []string{"http://localhost:1/cb"})
	code, _ := o.IssueCode(context.Background(), c, "alice", "http://localhost:1/cb", []string{"apptown:user:read"}, "", "ch")
	rec, _ := o.CodeByValue(context.Background(), code)
	did, pair, _, err := o.Exchange(context.Background(), code, rec, c.Name, "internal")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := o.Token(context.Background(), pair.Refresh, "refresh")
	if err != nil || rt == nil || rt.DelegationID != did || rt.Revoked {
		t.Fatalf("refresh record: %v %+v", err, rt)
	}
	next, err := s.RefreshForClient(context.Background(), pair.Refresh, c.ID)
	if err != nil || next.Refresh == pair.Refresh {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := s.RefreshForClient(context.Background(), pair.Refresh, c.ID); !errors.Is(err, ErrLeaked) {
		t.Fatalf("reuse: %v", err)
	}
	if conn, _ := o.Connection(context.Background(), did); conn.LiveAt(time.Now()) {
		t.Fatal("a leak ends the delegation")
	}
	if tok, _ := o.Token(context.Background(), next.Access, "access"); !tok.Revoked {
		t.Fatal("a leak revokes the live tokens")
	}
	code2, _ := o.IssueCode(context.Background(), c, "alice", "http://localhost:1/cb", []string{"apptown:user:read"}, "", "ch")
	rec2, _ := o.CodeByValue(context.Background(), code2)
	did2, pair2, _, _ := o.Exchange(context.Background(), code2, rec2, c.Name, "internal")
	if got, found, err := o.DelegationOfToken(context.Background(), pair2.Access); err != nil || !found || got != did2 {
		t.Fatalf("delegation of a token: %v %v %d", err, found, got)
	}
	if _, found, _ := o.DelegationOfToken(context.Background(), "nope"); found {
		t.Fatal("unknown token")
	}
	if _, err := s.ResolveOAuth(context.Background(), pair2.Access, AudienceUser); err != nil {
		t.Fatal(err)
	}
	mine, err := o.Connections(context.Background(), "alice", false)
	if err != nil || len(mine) != 1 || mine[0].ID != did2 || !mine[0].LastUsed.Valid {
		t.Fatalf("my live connections: %v %+v", err, mine)
	}
	if all, _ := o.Connections(context.Background(), "nobody", true); len(all) != 1 {
		t.Fatalf("everyone's: %d", len(all))
	}
	if err := o.Revoke(context.Background(), did2, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := o.Revoke(context.Background(), idstest.N(9999), "alice"); err == nil {
		t.Fatal("revoking an unknown delegation is an error")
	}
	if mine, _ := o.Connections(context.Background(), "alice", false); len(mine) != 0 {
		t.Fatal("revoked connections are not listed")
	}
	if receipts := ledger(t, db, "oauth.disconnected"); len(receipts) != 1 {
		t.Fatalf("disconnect receipt: %+v", receipts)
	}
	var n int
	if err := o.SweepExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: No owner read lists authorization codes; the sweep's promise is that the table holds none.
	if err := db.QueryRow(`SELECT COUNT(*) FROM oauth_codes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the sweep drops used codes: %v %d", err, n)
	}
}

// A ticket whose recorded change or constraints cannot be read is refused,
// never honoured as a ticket for change 0 or for no ref: it does not
// resolve and cannot be consumed.
func TestADamagedTicketIsRefused(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice")
	k := NewTickets(db)
	for name, damage := range map[string]string{"change": `resource_id='forty'`, "constraints": `constraints_json='["damaged"]'`} {
		tok, ticket, err := k.Mint(context.Background(), "alice", ids.Nil, idstest.N(40), "refs/heads/apptown/change-40", "abc123")
		if err != nil {
			t.Fatal(err)
		}
		// sql-guarantee: no operation records a ticket it cannot read back; this proves a damaged one is refused.
		if _, err := db.Exec(`UPDATE object_capabilities SET `+damage+` WHERE id=$1`, ticket.ID); err != nil {
			t.Fatal(err)
		}
		if got, err := k.ByToken(context.Background(), tok); err == nil || got != nil {
			t.Fatalf("%s damaged: by token %+v %v; want an error", name, got, err)
		}
		if ok, err := k.Consume(context.Background(), ticket.ID); err == nil || ok {
			t.Fatalf("%s damaged: consumed %v %v; want an error", name, ok, err)
		}
	}
}

// A git ticket: minted once, live until consumed, expired or revoked;
// consumed exactly once; revoked with its change.
func TestTickets(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	testAccounts(t, db, "alice")
	k := NewTickets(db)
	tok, ticket, err := k.Mint(context.Background(), "alice", ids.Nil, idstest.N(40), "refs/heads/apptown/change-40", "abc123")
	if err != nil || !strings.HasPrefix(tok, TicketPrefix) || ticket.ID.IsZero() {
		t.Fatalf("mint: %v %q %+v", err, tok, ticket)
	}
	live, err := k.ByToken(context.Background(), tok)
	if err != nil || live == nil || live.ChangeID != idstest.N(40) || live.Ref != "refs/heads/apptown/change-40" || live.ExpectedTip != "abc123" {
		t.Fatalf("by token: %v %+v", err, live)
	}
	if got, err := k.ByToken(context.Background(), "apptown_git_nope"); err != nil || got != nil {
		t.Fatalf("unknown ticket: %v %+v", err, got)
	}
	if got, _ := k.ByToken(context.Background(), "not-a-ticket"); got != nil {
		t.Fatal("wrong prefix")
	}
	if ok, err := k.Consume(context.Background(), ticket.ID); err != nil || !ok {
		t.Fatalf("consume: %v %v", err, ok)
	}
	if ok, err := k.Consume(context.Background(), ticket.ID); err != nil || ok {
		t.Fatalf("a spent ticket is not consumed twice: %v %v", err, ok)
	}
	if got, _ := k.ByToken(context.Background(), tok); got != nil {
		t.Fatal("a spent ticket is not live")
	}
	tok2, _, _ := k.Mint(context.Background(), "alice", ids.Nil, idstest.N(40), "refs/heads/apptown/change-40", "")
	if err := k.RevokeForChange(context.Background(), idstest.N(40)); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.ByToken(context.Background(), tok2); got != nil {
		t.Fatal("a revoked ticket is not live")
	}
	tok3, t3, _ := k.Mint(context.Background(), "alice", ids.Nil, idstest.N(41), "refs/heads/apptown/change-41", "")
	ticketRow, err := queries(db).TicketByIDForUpdate(context.Background(), t3.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queries(db).ConsumeTicket(context.Background(), accesssql.ConsumeTicketParams{
		ID: ticketRow.ID, Kind: ticketRow.Kind, DelegationID: ticketRow.DelegationID, ResourceType: ticketRow.ResourceType, ResourceID: ticketRow.ResourceID,
		ConstraintsJson: ticketRow.ConstraintsJson, TokenHash: ticketRow.TokenHash, CreatedAt: ticketRow.CreatedAt, ExpiresAt: time.Now().Add(-time.Minute), ConsumedAt: ticketRow.ConsumedAt, RevokedAt: ticketRow.RevokedAt,
	})
	if err != nil || rows != 1 {
		t.Fatalf("expire live ticket: %d %v", rows, err)
	}
	if got, _ := k.ByToken(context.Background(), tok3); got != nil {
		t.Fatal("an expired ticket is not live")
	}
}

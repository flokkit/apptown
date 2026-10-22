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

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/clock/clocktest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// An invite, a delegation and an administrator's grant are written to
// expire by AppTown's clock and judged by it where they are used
// (accepting an invite, a delegation's Live, ActiveGrants), so the listing
// of pending invites, the sweep of expired delegations and the count of
// administrators judge by that clock too. With the database's clock forty
// days ahead, an invite and a delegation still live by AppTown's clock
// are listed and kept, and an administrator whose grant ends in an hour
// still counts.
func TestAppTownsClockDecidesWhatExpires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := testInviteService(t)
	if _, err := s.Invite(ctx, NewInvite{Username: "kim", Label: "Kim Lee"}, testPrincipal("ops")); err != nil {
		t.Fatal(err)
	}
	c, err := s.RegisterOAuthClient(ctx, Registration{Name: "agent", RedirectURIs: []string{"http://localhost:1/cb"}})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	b := BrowserAuthorization{User: "ops", Binding: "browser", Request: AuthorizationRequest{ClientID: c.ID, RedirectURI: c.RedirectURIs[0], Scope: "apptown:user:read", Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Method: "S256"}}
	prepared, err := s.PrepareConsent(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DecideConsent(ctx, ConsentDecision{Browser: b, Token: prepared.Token, Choice: ConsentAllow})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := s.ExchangeCode(ctx, CodeExchange{Code: result.Code, Verifier: verifier, ClientID: c.ID, RedirectURI: b.Request.RedirectURI, AuthMethod: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	delegation, found, err := s.oauth.DelegationOfToken(ctx, pair.Access)
	if err != nil || !found {
		t.Fatalf("delegation: %v %v", found, err)
	}
	if _, err := s.EnsureUser(ctx, "lee"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGrants(s.db).Add(ctx, Spec{SubjectType: "user", Subject: "lee", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "ops"}, time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	before, err := enabledRoots(ctx, s.db, time.Now())
	if err != nil || before != 2 {
		t.Fatalf("administrators before: %d %v", before, err)
	}

	moveDatabaseClock(t, s.db, "40 days")
	if pending, err := s.Invites(ctx, testPrincipal("ops")); err != nil || len(pending) != 1 || pending[0].State != "pending" {
		t.Fatalf("a pending invite by AppTown's clock: %+v %v", pending, err)
	}
	if err := s.oauth.SweepExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if conn, err := s.oauth.Connection(ctx, delegation); err != nil || conn == nil || !conn.LiveAt(time.Now()) {
		t.Fatalf("a live delegation by AppTown's clock was swept: %+v %v", conn, err)
	}
	if n, err := enabledRoots(ctx, s.db, time.Now()); err != nil || n != before {
		t.Fatalf("administrators with the database's clock ahead: %d %v, want %d", n, err, before)
	}
}

// A revoked token is kept a day after its revocation by AppTown's clock,
// which records the revocation and sweeps it: with the database's clock two
// days behind, a token revoked just now survives the sweep.
func TestARevokedTokenIsKeptADayByAppTownsClock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := New(testDB(t), Config{})
	testAccounts(t, s.db, "alice")
	c, err := s.RegisterOAuthClient(ctx, Registration{Name: "agent", RedirectURIs: []string{"http://localhost:1/cb"}})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	connect := func() (CodeExchange, Pair) {
		t.Helper()
		b := BrowserAuthorization{User: "alice", Binding: "browser", Request: AuthorizationRequest{ClientID: c.ID, RedirectURI: c.RedirectURIs[0], Scope: "apptown:user:read", Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Method: "S256"}}
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
		if err != nil {
			t.Fatal(err)
		}
		return in, pair
	}
	moveDatabaseClock(t, s.db, "-2 days")
	// Revoked two ways: the person disconnects the agent, and a replayed
	// code ends what it minted.
	_, disconnected := connect()
	delegation, _, err := s.oauth.DelegationOfToken(ctx, disconnected.Access)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.oauth.Revoke(ctx, delegation, "alice"); err != nil {
		t.Fatal(err)
	}
	replay, replayed := connect()
	if _, err := s.ExchangeCode(ctx, replay); !errors.Is(err, ErrCodeReplay) {
		t.Fatalf("a second exchange is a replay: %v", err)
	}
	if err := s.oauth.SweepExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string]Pair{"disconnected": disconnected, "replayed": replayed} {
		if tok, err := s.oauth.Token(ctx, pair.Access, "access"); err != nil || tok == nil || !tok.Revoked {
			t.Errorf("%s: a token revoked just now was swept, or is not revoked: %+v %v", name, tok, err)
		}
	}
}

// movableClock is an AppTown clock a test sets and moves.
type movableClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *movableClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *movableClock) move(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(by)
}

// Every lifetime identity gives a credential is written and judged by the
// AppTown's clock, never the wall clock or the database's: on an AppTown clock
// ninety days behind both, a session, an invite, a consent request, a
// code, an access and a refresh token, a git ticket and an expiring grant
// all live their lifetimes, and each ends when that clock passes it.
// The connection's own thirty days end it before its last refresh token's.
func TestEveryLifetimeIsJudgedByAppTownsClock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := &movableClock{at: time.Now().Add(-90 * 24 * time.Hour).UTC()}
	s := New(testDB(t), Config{Now: clk.now})
	signedIn, err := s.Bootstrap(ctx, NewAccount{Username: "ops", Password: "a-long-password"})
	if err != nil {
		t.Fatal(err)
	}
	invited, err := s.Invite(ctx, NewInvite{Username: "kim", Label: "Kim Lee"}, testPrincipal("ops"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.RegisterOAuthClient(ctx, Registration{Name: "agent", RedirectURIs: []string{"http://localhost:1/cb"}})
	if err != nil {
		t.Fatal(err)
	}
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	browser := BrowserAuthorization{User: "ops", Binding: "browser", Request: AuthorizationRequest{ClientID: c.ID, RedirectURI: c.RedirectURIs[0], Scope: "apptown:user:read", Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Method: "S256"}}
	code := func() (PreparedConsent, string) {
		t.Helper()
		prepared, err := s.PrepareConsent(ctx, browser)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.DecideConsent(ctx, ConsentDecision{Browser: browser, Token: prepared.Token, Choice: ConsentAllow})
		if err != nil {
			t.Fatalf("a consent request just made, by AppTown's clock: %v", err)
		}
		return prepared, result.Code
	}
	exchange := func(code string) (Pair, error) {
		return s.ExchangeCode(ctx, CodeExchange{Code: code, Verifier: verifier, ClientID: c.ID, RedirectURI: browser.Request.RedirectURI, AuthMethod: "internal"})
	}
	_, first := code()
	pair, err := exchange(first)
	if err != nil {
		t.Fatalf("a code just issued, by AppTown's clock: %v", err)
	}
	_, unexchanged := code()
	undecided, err := s.PrepareConsent(ctx, browser)
	if err != nil {
		t.Fatal(err)
	}
	delegation, _, err := s.oauth.DelegationOfToken(ctx, pair.Access)
	if err != nil {
		t.Fatal(err)
	}
	ticket, _, err := s.IssueTicket(ctx, "ops", delegation, idstest.N(1), "refs/heads/apptown/change-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(ctx, Spec{SubjectType: "user", Subject: "lee", Role: access.RoleAuditor, Scope: access.ScopeInstance, By: "ops"}, clk.now().Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	lee := access.Principal{Kind: "user", Name: "lee"}
	session := func() bool {
		t.Helper()
		pr, err := s.SessionIdentity(ctx, signedIn.Token)
		if err != nil {
			t.Fatal(err)
		}
		return pr.Name == "ops"
	}
	inviteState := func() string {
		t.Helper()
		inv, err := s.InviteFor(ctx, invited.Token)
		if err != nil {
			t.Fatal(err)
		}
		return inv.State
	}
	granted := func() bool {
		t.Helper()
		grants, err := s.ActiveGrants(ctx, lee)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range grants {
			if g.Subject == "lee" && g.RoleName == access.RoleAuditor {
				return true
			}
		}
		return false
	}
	ticketLive := func() bool {
		t.Helper()
		k, err := s.TicketByToken(ctx, ticket)
		if err != nil {
			t.Fatal(err)
		}
		return k != nil
	}

	if !session() || inviteState() != "pending" || !granted() || !ticketLive() {
		t.Fatalf("live by AppTown's clock: session %v, invite %s, grant %v, ticket %v", session(), inviteState(), granted(), ticketLive())
	}
	if _, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser); err != nil {
		t.Fatalf("an access token live by AppTown's clock: %v", err)
	}

	// Two hours on: the short lifetimes have ended, the long ones have not.
	clk.move(2 * time.Hour)
	if _, err := s.ResolveOAuth(ctx, pair.Access, AudienceUser); !errors.Is(err, ErrOAuthCredential) {
		t.Fatalf("an access token past its hour: %v", err)
	}
	if ticketLive() {
		t.Fatal("a git ticket past its lifetime")
	}
	if granted() {
		t.Fatal("a grant past its expiry")
	}
	if _, err := exchange(unexchanged); err == nil {
		t.Fatal("a code past its lifetime was exchanged")
	}
	if _, err := s.DecideConsent(ctx, ConsentDecision{Browser: browser, Token: undecided.Token, Choice: ConsentAllow}); err == nil {
		t.Fatal("a consent request past its lifetime was decided")
	}
	if !session() || inviteState() != "pending" {
		t.Fatalf("a session and an invite within their lifetimes: session %v, invite %s", session(), inviteState())
	}
	refreshed, err := s.Refresh(ctx, pair.Refresh)
	if err != nil {
		t.Fatalf("a refresh token within its lifetime: %v", err)
	}

	// An hour past thirty days: the session, the invite and the connection
	// have ended, though the refresh token minted at two hours has not.
	clk.move(30*24*time.Hour - time.Hour)
	if session() {
		t.Fatal("a session past its lifetime")
	}
	if got := inviteState(); got != "expired" {
		t.Fatalf("an invite past its lifetime: %s", got)
	}
	if _, err := s.Refresh(ctx, refreshed.Refresh); !errors.Is(err, ErrConnectionEnded) {
		t.Fatalf("a refresh past the connection's lifetime: %v", err)
	}
}

// Who administers is judged by AppTown's clock when a grant is revoked:
// on a clock ninety days behind the wall clock, an administrator whose grant
// ends in an hour is still one, so the other may lose theirs, and is then
// the last. A grant is stamped with that clock too.
func TestTheLastAdministratorIsJudgedByAppTownsClock(t *testing.T) {
	t.Parallel()
	clk := &movableClock{at: time.Now().Add(-90 * 24 * time.Hour).UTC().Truncate(time.Second)}
	s := New(testDB(t), Config{Now: clk.now})
	testGrantRoot(t, s, "ops")
	if _, err := s.EnsureUser(context.Background(), "may"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "may", Role: access.RoleInstanceAdmin, Scope: access.ScopeInstance, By: "ops"}, clk.now().Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	grants, err := s.GrantsAt(context.Background(), access.ScopeInstance, "")
	if err != nil {
		t.Fatal(err)
	}
	var ops, may ids.ID
	for _, g := range grants {
		if g.RoleName != access.RoleInstanceAdmin {
			continue
		}
		if at, err := time.Parse(time.RFC3339Nano, g.CreatedAt); err != nil || !at.Equal(clk.now()) {
			t.Fatalf("grant %d stamped %s, not AppTown's clock %s: %v", g.ID, g.CreatedAt, clk.now(), err)
		}
		switch g.Subject {
		case "ops":
			ops = g.ID
		case "may":
			may = g.ID
		}
	}
	if revoked, err := s.Revoke(context.Background(), ops, "ops"); err != nil || !revoked {
		t.Fatalf("may administers for another hour by AppTown's clock: %v %v", revoked, err)
	}
	if revoked, err := s.Revoke(context.Background(), may, "may"); !errors.Is(err, ErrLastAdministrator) || revoked {
		t.Fatalf("may is the last administrator by AppTown's clock: %v %v", revoked, err)
	}
}

// An operation takes one reading of AppTown's clock, and its record and
// its receipt both carry that reading. On a clock that moves on a second
// at every reading, a receipt stamped by a second reading would say a
// later second than the record it describes.
func TestAnOperationsReceiptCarriesItsRecordsTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := clocktest.NewStepping(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), time.Second)
	s := New(testDB(t), Config{SessionTTL: time.Hour, Now: clk.Now})
	testAccounts(t, s.db, "alice", "ops")
	testGrantRoot(t, s, "ops")
	ops := testPrincipal("ops")
	receiptAt := func(kind string) time.Time {
		t.Helper()
		got := receipts(t, s, kind)
		if len(got) == 0 {
			t.Fatalf("no %s receipt", kind)
		}
		at, err := time.Parse(time.RFC3339Nano, got[0].At)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	same := func(kind string, record, receipt time.Time) {
		t.Helper()
		if !record.Truncate(time.Second).Equal(receipt.Truncate(time.Second)) {
			t.Errorf("%s: record at %v, receipt at %v", kind, record, receipt)
		}
	}

	reading := clk.Next()
	issued, err := s.Invite(ctx, NewInvite{Username: "kim", Label: "Kim Lee"}, ops)
	if err != nil {
		t.Fatal(err)
	}
	same("user.invited", issued.Invite.CreatedAt, receiptAt("user.invited"))
	same("user.invited reading", reading, issued.Invite.CreatedAt)

	reading = clk.Next()
	if err := s.SaveGroup(ctx, GroupChange{Name: "operators", Members: []string{"ops"}}, ops); err != nil {
		t.Fatal(err)
	}
	same("group.saved", reading, receiptAt("group.saved"))

	reading = clk.Next()
	if _, err := s.RegisterOAuthClient(ctx, Registration{Name: "agent", RedirectURIs: []string{"http://localhost:1/cb"}}); err != nil {
		t.Fatal(err)
	}
	same("oauth.client-registered", reading, receiptAt("oauth.client-registered"))

	_, ticket, err := s.IssueTicket(ctx, "alice", ids.Nil, idstest.N(40), "refs/heads/apptown/change-40", "abc")
	if err != nil {
		t.Fatal(err)
	}
	reading = clk.Next()
	if ok, err := s.ConsumeTicket(ctx, ticket.ID); err != nil || !ok {
		t.Fatalf("consumed %v: %v", ok, err)
	}
	same("git-ticket.consumed", reading, receiptAt("git-ticket.consumed"))
}

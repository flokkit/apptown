package identity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
)

func tokenCommandFixture(t *testing.T) (*Service, access.Principal) {
	t.Helper()
	s := New(testDB(t), Config{})
	if err := s.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	return s, admittedPrincipal(t, s, "boss")
}

func TestTokenCommandsCheckCurrentAdministrator(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"issue", "list", "revoke"} {
		for _, caller := range []string{"admin", "admin token", "cli token", "proxy", "member", "disabled", "bounded token", "agent token", "oauth", "instrument", "missing", "open"} {
			t.Run(op+"/"+caller, func(t *testing.T) {
				t.Parallel()
				s, pr := tokenCommandFixture(t)
				if _, err := s.IssueToken(context.Background(), NewToken{Purpose: "cli", Name: "existing"}, pr); err != nil {
					t.Fatal(err)
				}
				allowed := caller == "admin" || caller == "admin token" || caller == "cli token" || caller == "proxy"
				switch caller {
				case "admin token":
					pr.Via = "token"
				case "cli token":
					pr.Via, pr.Role, pr.Purpose = "token", "admin", access.TokenCLI
				case "agent token":
					// An administrator's agent token: minting a cli token
					// here would hand an agent its person's full authority.
					pr.Via, pr.Role, pr.Purpose = "token", "admin", access.TokenAgent
				case "proxy":
					pr.Via = "proxy"
				case "member":
					if _, err := s.EnsureUser(context.Background(), "member"); err != nil {
						t.Fatal(err)
					}
					if _, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "member", Capability: string(access.CapIdentityUsers), Scope: access.ScopeInstance, By: "setup"}, ""); err != nil {
						t.Fatal(err)
					}
					pr = admittedPrincipal(t, s, "member") // advertised admin role must not override the account
				case "disabled":
					disableStoredAccount(t, s.db, "boss")
				case "bounded token":
					pr.Via, pr.Role = "token", "member"
				case "oauth":
					pr.Via, pr.Scopes = "oauth", access.ScopeNames()
				case "instrument":
					pr.Kind, pr.Instrument, pr.Person = "instrument", "console", "boss"
				case "missing":
					pr = access.Principal{}
				case "open":
					s = New(s.db, Config{OpenInstance: true})
					pr = testOpenPrincipal("installer")
				}
				var err error
				switch op {
				case "issue":
					var issued issuedToken
					issued, err = s.IssueToken(context.Background(), NewToken{Purpose: "cli", Name: "new", Role: "admin"}, pr)
					if !allowed && issued.Plaintext != "" {
						t.Fatal("denied issue exposed a credential")
					}
				case "list":
					var all []Token
					all, err = s.Tokens(context.Background(), pr)
					if !allowed && len(all) != 0 {
						t.Fatal("denied list exposed token metadata")
					}
				case "revoke":
					var revoked bool
					revoked, err = s.RevokeToken(context.Background(), "existing", pr)
					if !allowed && revoked {
						t.Fatal("denied revoke reported success")
					}
				}
				if allowed {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				var denied *access.Denied
				if !errors.As(err, &denied) && !errors.Is(err, access.ErrPrincipalRequired) {
					t.Fatalf("expected denial: %v", err)
				}
				live := 0
				for _, token := range storedTokens(t, s.db) {
					if !token.RevokedAt.Valid {
						live++
					}
				}
				if live != 1 {
					t.Fatalf("denied mutation: %d live tokens", live)
				}
				if count := tokenReceipts(t, s.db); count != 1 {
					t.Fatalf("denied receipt: %d", count)
				}
			})
		}
	}
}

// A token is made for a command line or for an agent, never by default; the
// purpose is kept, reported, and carried into the principal the token
// resolves to.
func TestTokenPurposeIsChosenKeptAndRequired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, pr := tokenCommandFixture(t)
	for _, purpose := range []string{"", "robot"} {
		if _, err := s.IssueToken(ctx, NewToken{Name: "unchosen", Purpose: purpose}, pr); !errors.Is(err, errTokenPurpose) {
			t.Fatalf("purpose %q: %v", purpose, err)
		}
	}
	for _, purpose := range []string{access.TokenCLI, access.TokenAgent} {
		issued, err := s.IssueToken(ctx, NewToken{Name: purpose, Purpose: purpose}, pr)
		if err != nil || issued.Token.Purpose != purpose {
			t.Fatalf("%s: %v %+v", purpose, err, issued.Token)
		}
		resolved, err := s.PersonalTokenIdentity(ctx, issued.Plaintext, nil)
		if err != nil || resolved.Purpose != purpose || resolved.Via != "token" {
			t.Fatalf("%s resolves to %+v %v", purpose, resolved, err)
		}
	}
	all, err := s.Tokens(ctx, pr)
	if err != nil || len(all) != 2 || all[0].Purpose == "" || all[1].Purpose == "" {
		t.Fatalf("listing: %+v %v", all, err)
	}
	for _, purpose := range []string{access.TokenCLI, access.TokenAgent} {
		receipts, err := audit.Recent(ctx, s.db, audit.Filter{KindPrefix: "token.created", Subject: purpose})
		if err != nil || len(receipts) != 1 || !strings.Contains(string(receipts[0].Payload), `"purpose":"`+purpose+`"`) {
			t.Fatalf("the %s receipt names its purpose: %+v %v", purpose, receipts, err)
		}
	}
}

func TestTokenIssueOwnsMetadataAndCredentialBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, pr := tokenCommandFixture(t)
	testAccounts(t, s.db, "boss")
	issued, err := s.IssueToken(ctx, NewToken{Purpose: "cli", Name: "default", Role: "unknown"}, pr)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Token.User != "boss" || issued.Token.Role != "member" || !strings.HasPrefix(issued.Plaintext, TokenPrefix) {
		t.Fatal("incorrect issuance defaults")
	}
	if _, err := s.IssueToken(ctx, NewToken{Purpose: "cli", Name: "default"}, pr); err == nil {
		t.Fatal("duplicate name allowed")
	}
	other, err := s.IssueToken(ctx, NewToken{Purpose: "cli", Name: "other", User: "future-owner", Role: "admin"}, pr)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.PersonalTokenIdentity(ctx, other.Plaintext, nil)
	if err != nil || resolved.Name != "future-owner" || resolved.Role != "member" {
		t.Fatalf("owner standing not applied: %v", err)
	}
	disableStoredAccount(t, s.db, "future-owner")
	if _, err := s.PersonalTokenIdentity(ctx, other.Plaintext, nil); err == nil {
		t.Fatal("disabled owner admitted")
	}
	all, err := s.Tokens(ctx, pr)
	if err != nil || len(all) != 2 {
		t.Fatalf("metadata list: %v", err)
	}
	metadata, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), issued.Plaintext) || strings.Contains(string(metadata), HashToken(issued.Plaintext)) {
		t.Fatal("credential material in metadata")
	}
	stored := ""
	for _, token := range storedTokens(t, s.db) {
		if token.Name == "default" {
			stored = token.TokenHash
		}
	}
	if stored != HashToken(issued.Plaintext) {
		t.Fatal("stored credential is not its hash")
	}
	created := ledger(t, s.db, "token.created")
	if len(created) == 0 {
		t.Fatal("issuance left no receipt")
	}
	for _, receipt := range created {
		actor, body := receipt.Actor, string(receipt.Payload)
		if actor != "boss" || strings.Contains(body, issued.Plaintext) || strings.Contains(body, stored) || strings.Contains(body, other.Plaintext) {
			t.Fatal("incorrect receipt attribution or exposed credential")
		}
	}
}

func TestTokenCommandsRequirePersistenceAndReceipts(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"issue", "revoke"} {
		for _, failure := range []string{"account", "write", "receipt", "skipped write", "skipped receipt", "cancelled"} {
			t.Run(op+"/"+failure, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				s, pr := tokenCommandFixture(t)
				existing, err := s.IssueToken(ctx, NewToken{Purpose: "cli", Name: "existing"}, pr)
				if err != nil {
					t.Fatal(err)
				}
				marker := map[string]string{"account": "-- name: UserByPrincipal :one", "receipt": "INSERT INTO audit_log"}[failure]
				operation := "INSERT"
				if op == "revoke" {
					operation = "UPDATE"
				}
				if failure == "write" {
					marker = "-- name: InsertAuthToken"
					if op == "revoke" {
						marker = "-- name: RevokeAuthToken"
					}
				}
				restore := func() {}
				if marker != "" {
					restore = dbtest.NextStatementFailure(s.db, marker)
				}
				if failure == "skipped write" {
					restore = vetoGroupWrite(t, s.db, "auth_tokens", operation)
				}
				if failure == "skipped receipt" {
					restore = vetoGroupWrite(t, s.db, "audit_log", "INSERT")
				}
				if failure == "cancelled" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				call := func(ctx context.Context) error {
					if op == "issue" {
						issued, err := s.IssueToken(ctx, NewToken{Purpose: "cli", Name: "new"}, pr)
						if err != nil && issued.Plaintext != "" {
							t.Fatal("failed commit released plaintext")
						}
						return err
					}
					revoked, err := s.RevokeToken(ctx, "existing", pr)
					if err != nil && revoked {
						t.Fatal("failed commit reported revocation")
					}
					return err
				}
				if err := call(ctx); err == nil {
					t.Fatal("accepted incomplete command")
				}
				restore()
				if stored := storedTokens(t, s.db); len(stored) != 1 {
					t.Fatalf("partial issuance: %d", len(stored))
				}
				if _, _, _, ok, err := s.tokens.resolveToken(context.Background(), existing.Plaintext); err != nil || !ok {
					t.Fatal("failed revoke ended live token")
				}
				if count := tokenReceipts(t, s.db); count != 1 {
					t.Fatalf("false receipt: %d", count)
				}
				if err := call(context.Background()); err != nil {
					t.Fatalf("retry: %v", err)
				}
			})
		}
	}
}

func TestTokenRevocationIsOneWayAndConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, pr := tokenCommandFixture(t)
	issued, err := s.IssueToken(ctx, NewToken{Purpose: "cli", Name: "cli"}, pr)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		revoked bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 8)
	for range 8 {
		go func() { <-start; ok, err := s.RevokeToken(ctx, "cli", pr); results <- result{ok, err} }()
	}
	close(start)
	winners := 0
	for range 8 {
		r := <-results
		if r.err != nil {
			t.Error(r.err)
		}
		if r.revoked {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("revocation winners: %d", winners)
	}
	if pr, err := s.PersonalTokenIdentity(ctx, issued.Plaintext, nil); err != nil || pr.Name != "" {
		t.Fatal("revoked credential still resolves")
	}
	for _, name := range []string{"cli", "missing"} {
		if ok, err := s.RevokeToken(ctx, name, pr); err != nil || ok {
			t.Fatalf("repeated/absent revocation: %v %v", ok, err)
		}
	}
	all, err := s.Tokens(ctx, pr)
	if err != nil || len(all) != 1 || all[0].live() {
		t.Fatal("revoked metadata not retained")
	}
	if receipts := ledger(t, s.db, "token.revoked"); len(receipts) != 1 {
		t.Fatalf("revocation receipts: %+v", receipts)
	}
}

func TestTokenReadFailureCannotReturnMetadataOrRevoke(t *testing.T) {
	t.Parallel()
	s, pr := tokenCommandFixture(t)
	ctx := context.Background()
	if _, err := s.IssueToken(ctx, NewToken{Purpose: "cli", Name: "cli"}, pr); err != nil {
		t.Fatal(err)
	}
	restore := dbtest.NextStatementFailure(s.db, "-- name: AuthTokensAll")
	all, err := s.Tokens(ctx, pr)
	restore()
	if err == nil || all != nil {
		t.Fatal("failed list returned metadata")
	}
	restore = dbtest.NextStatementFailure(s.db, "-- name: AuthTokenByNameForUpdate")
	revoked, err := s.RevokeToken(ctx, "cli", pr)
	restore()
	if err == nil || revoked {
		t.Fatal("failed lookup reported revocation")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if all, err := s.Tokens(cancelled, pr); err == nil || all != nil {
		t.Fatal("cancelled list returned metadata")
	}
	if ok, err := s.RevokeToken(ctx, "cli", pr); err != nil || !ok {
		t.Fatalf("retry: %v %v", ok, err)
	}
}

// A personal account sees only its own live token metadata, without gaining
// the administrator's token catalogue or exposing credential material.
func TestOwnTokensKeepAccountBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, boss := tokenCommandFixture(t)
	if _, err := s.EnsureUser(ctx, "member"); err != nil {
		t.Fatal(err)
	}
	for _, in := range []NewToken{{Name: "boss-cli", Purpose: "cli"}, {Name: "member-cli", User: "member", Purpose: "cli"}, {Name: "old", User: "member", Purpose: "agent"}} {
		if _, err := s.IssueToken(ctx, in, boss); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RevokeToken(ctx, "old", boss); err != nil {
		t.Fatal(err)
	}
	member := admittedPrincipal(t, s, "member")
	member.Credential = HeaderCredential("member")
	tokens, err := s.OwnTokens(ctx, member)
	if err != nil || len(tokens) != 1 || tokens[0].Name != "member-cli" {
		t.Fatalf("own tokens: %+v %v", tokens, err)
	}
	if _, err := s.Tokens(ctx, member); err == nil {
		t.Fatal("personal read granted catalogue access")
	}
	for _, pr := range []access.Principal{{}, {Kind: "instrument", Name: "member"}, {Kind: "user", Name: "member"}} {
		if _, err := s.OwnTokens(ctx, pr); err == nil {
			t.Fatal("unverified caller read tokens")
		}
	}
	disableStoredAccount(t, s.db, "member")
	if _, err := s.OwnTokens(ctx, member); err == nil {
		t.Fatal("disabled account read tokens")
	}
}

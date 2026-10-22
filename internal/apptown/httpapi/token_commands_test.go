package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
)

func accountHTTPFixture(t *testing.T) (*database.DB, *identity.Service) {
	t.Helper()
	db := databaseSuite.New(t)
	s := identity.New(db, identity.Config{})
	if err := s.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureUser(context.Background(), "kid"); err != nil {
		t.Fatal(err)
	}
	return db, s
}
func tokenCall(h Accounts, who, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: "session"}, Verified: true}))
	r.SetPathValue("name", strings.TrimPrefix(path, "/api/v1/tokens/"))
	w := httptest.NewRecorder()
	switch method {
	case "GET":
		h.ListTokens(w, r)
	case "POST":
		h.CreateToken(w, r)
	case "DELETE":
		h.RevokeToken(w, r)
	}
	return w
}
func vetoHTTPRecord(t *testing.T, db *database.DB, table, operation, body string) func() {
	t.Helper()
	exec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE FUNCTION veto_authorization_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN ` + body + ` END $$`)
	exec(`CREATE TRIGGER veto_authorization_record BEFORE ` + operation + ` ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION veto_authorization_record()`)
	return func() { exec(`DROP TRIGGER veto_authorization_record ON ` + table) }
}

func TestTokenHTTPKeepsMetadataAndRoleBounds(t *testing.T) {
	t.Parallel()
	db, accounts := accountHTTPFixture(t)
	_ = db
	h := NewAccounts(accounts)
	w := tokenCall(h, "boss", "POST", "/api/v1/tokens", `{"name":"default","role":"unknown","purpose":"cli"}`)
	if w.Code != 201 {
		t.Fatalf("issuance: %d", w.Code)
	}
	var issued struct{ Token, Name, User, Role string }
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Name != "default" || issued.User != "boss" || issued.Role != "member" || issued.Token == "" {
		t.Fatal("incorrect issuance metadata")
	}
	w = tokenCall(h, "boss", "GET", "/api/v1/tokens", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), issued.Token) || strings.Contains(w.Body.String(), identity.HashToken(issued.Token)) {
		t.Fatalf("unsafe listing: %d", w.Code)
	}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		path := "/api/v1/tokens"
		if method == "DELETE" {
			path += "/default"
		}
		if w := tokenCall(h, "kid", method, path, `{"name":"escalate","user":"kid","role":"admin"}`); w.Code != 403 {
			t.Fatalf("member %s: %d", method, w.Code)
		}
	}
	meta, err := accounts.Tokens(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "session"})
	if err != nil || len(meta) != 1 || meta[0].Name != "default" {
		t.Fatalf("denied token commands changed live tokens: %+v %v", meta, err)
	}
	if w := tokenCall(h, "boss", "POST", "/api/v1/tokens", `{}`); w.Code != 400 {
		t.Fatalf("missing name: %d", w.Code)
	}
	if w := tokenCall(h, "boss", "DELETE", "/api/v1/tokens/default", ""); w.Code != 200 {
		t.Fatalf("revocation: %d", w.Code)
	}
	if w := tokenCall(h, "boss", "DELETE", "/api/v1/tokens/default", ""); w.Code != 404 {
		t.Fatalf("repeat revocation: %d", w.Code)
	}
}
func TestTokenHTTPSkippedRevocationIsPersistenceFailure(t *testing.T) {
	t.Parallel()
	db, accounts := accountHTTPFixture(t)
	_ = db
	h := NewAccounts(accounts)
	if _, err := accounts.IssueToken(context.Background(), identity.NewToken{Purpose: "cli", Name: "cli", User: "boss", Role: "member"}, access.Principal{Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	drop := vetoHTTPRecord(t, db, "auth_tokens", "UPDATE", "RETURN NULL;")
	w := tokenCall(h, "boss", "DELETE", "/api/v1/tokens/cli", "")
	drop()
	if w.Code != 500 {
		t.Fatalf("skipped live revocation must be an error, got %d", w.Code)
	}
	tokens, err := accounts.Tokens(context.Background(), access.Principal{Kind: "user", Name: "boss", Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, token := range tokens {
		if token.Name == "cli" && !token.RevokedAt.Valid {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("skipped revocation changed the token: %+v", tokens)
	}
	if receipts := receiptsOfKind(t, db, "token.revoked"); len(receipts) != 0 {
		t.Fatalf("receipt without a revocation: %+v", receipts)
	}
	if w := tokenCall(h, "boss", "DELETE", "/api/v1/tokens/cli", ""); w.Code != 200 {
		t.Fatalf("retry: %d", w.Code)
	}
}

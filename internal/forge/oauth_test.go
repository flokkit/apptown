package forge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
)

// The OAuth door for the MCP (design/subsystems/mcp.md): discovery,
// registration, consent in the browser, PKCE, audience-bound tokens that
// are bounds on live authority, rotation, revocation.

func oauthFixture(t *testing.T, configs ...Config) (*App, http.Handler, *http.Cookie) {
	t.Helper()
	a, h := internalApp(t, configs...)
	admin := sessionFrom(t, postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "boss-password")))
	if w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "p", "name": "P"}, admin); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	return a, h, admin
}

func pkce() (verifier, challenge string) {
	verifier = strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func registerClient(t *testing.T, h http.Handler) string {
	t.Helper()
	w := postJSON(h, "/oauth/register", map[string]any{"client_name": "Claude Code", "redirect_uris": []string{"http://127.0.0.1:9999/callback"}})
	if w.Code != 201 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out.ClientID
}

func consentToken(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	m := regexp.MustCompile(`name="consent_token" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if w.Code != 200 || len(m) != 2 {
		t.Fatalf("consent page: %d %s", w.Code, w.Body.String())
	}
	return html.UnescapeString(m[1])
}

func prepareConsent(t *testing.T, h http.Handler, who *http.Cookie, q url.Values) {
	t.Helper()
	req := httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(who)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	q.Set("consent_token", consentToken(t, w))
}

// authorizeAndExchange walks the browser consent as `who` and swaps the
// code for tokens. Returns the token response body.
func authorizeAndExchange(t *testing.T, h http.Handler, who *http.Cookie, clientID, scope string) map[string]any {
	t.Helper()
	verifier, challenge := pkce()
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:9999/callback"},
		"scope": {scope}, "state": {"xyz"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	// GET shows the consent page naming the client and what it may do.
	req := httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil)
	req.AddCookie(who)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Claude Code wants to use your AppTown account") {
		t.Fatalf("consent page: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `name="code_challenge" value="`+challenge+`"`) || strings.Contains(w.Body.String(), "action=\"/oauth/authorize?") {
		t.Fatalf("the consent form must carry the request as hidden fields, not a query string: %s", w.Body.String())
	}
	// POST allow (the browser sends the hidden fields in the body) → redirect with a code and the state.
	q.Set("consent_token", consentToken(t, w))
	q.Set("decision", "allow")
	req = httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(who)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 302 {
		t.Fatalf("decision: %d %s", w.Code, w.Body.String())
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc.Query().Get("state") != "xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("redirect: %s", w.Header().Get("Location"))
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "redirect_uri": {"http://127.0.0.1:9999/callback"},
		"client_id": {clientID}, "code_verifier": {verifier}}
	req = httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	var tok map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	return tok
}

// mcpWith sends one JSON-RPC body the way a client does: in a session it
// opened with initialize, unless the body is itself an initialize.
func mcpWith(h http.Handler, bearer, body string) *httptest.ResponseRecorder {
	sid := ""
	if !strings.Contains(body, `"method":"initialize"`) {
		init := mcpSend(h, bearer, "", `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"oauth-test","version":"1"}}}`)
		sid = init.Header().Get("Mcp-Session-Id")
	}
	return mcpSend(h, bearer, sid, body)
}

func mcpSend(h http.Handler, bearer, sid, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/mcp/user", strings.NewReader(body))
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	req.Header.Set("Content-Type", "application/json")
	// The streamable HTTP transport requires a client to accept both.
	req.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestOAuthDiscoveryAndRegistration(t *testing.T) {
	t.Parallel()
	_, h, _ := oauthFixture(t)
	req := httptest.NewRequest("GET", "/.well-known/oauth-protected-resource/mcp/user", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"authorization_servers"`) {
		t.Fatalf("resource metadata: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"S256"`) || !strings.Contains(w.Body.String(), "/oauth/register") {
		t.Fatalf("server metadata: %d %s", w.Code, w.Body.String())
	}
	// An unauthenticated MCP call points at the resource metadata.
	w = mcpWith(h, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("401 should advertise discovery: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	// A redirect to a non-loopback http host is refused.
	if w := postJSON(h, "/oauth/register", map[string]any{"client_name": "x", "redirect_uris": []string{"http://evil.example/cb"}}); w.Code != 400 {
		t.Errorf("non-loopback http redirect accepted: %d", w.Code)
	}
}

func TestOAuthFlowMintsBoundedTokens(t *testing.T) {
	t.Parallel()
	a, h, admin := oauthFixture(t)
	clientID := registerClient(t, h)
	// Not signed in: the page asks for a sign-in, never redirects a code.
	req := httptest.NewRequest("GET", "/oauth/authorize?response_type=code&client_id="+clientID+
		"&redirect_uri=http%3A%2F%2F127.0.0.1%3A9999%2Fcallback&code_challenge=abc&code_challenge_method=S256", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Sign in to connect") {
		t.Fatalf("anonymous authorize: %d %s", w.Code, w.Body.String())
	}
	tok := authorizeAndExchange(t, h, admin, clientID, "forge:user:read")
	accessToken, _ := tok["access_token"].(string)
	refresh, _ := tok["refresh_token"].(string)
	if !strings.HasPrefix(accessToken, identity.AccessPrefix) || refresh == "" || tok["scope"] != "forge:user:read" {
		t.Fatalf("token response: %v", tok)
	}
	// The access token opens the MCP door as boss and can read.
	w = mcpWith(h, accessToken, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"p"`) {
		t.Fatalf("mcp with oauth token: %d %s", w.Code, w.Body.String())
	}
	// It does NOT open the JSON API (audience-bound), and it is a bound, not
	// a role: the kernel refuses a write outside the consented scopes.
	req = httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("an MCP token must not open the JSON API: %d", w.Code)
	}
	id := a.resolveOAuthToken(context.Background(), accessToken, identity.AudienceUser)
	if id == nil || id.Via != "oauth" || id.User != "boss" {
		t.Fatalf("resolve: %+v", id)
	}
	pr := principalOf(id, "")
	p, _ := a.Projects.BySlug(context.Background(), "p")
	if a.holds(req.Context(), pr, access.CapChangeCreate, a.resourceProject(context.Background(), &p)) {
		t.Error("a read-only delegation must not create changes, whatever boss may do in person")
	}
	if !a.holds(req.Context(), pr, access.CapProjectRead, a.resourceProject(context.Background(), &p)) {
		t.Error("the delegation covers reads")
	}
	// Refresh rotates: the old refresh token dies, and reusing it ends the connection.
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}}
	req = httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("refresh: %d %s", w.Code, w.Body.String())
	}
	var tok2 map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tok2)
	req = httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("reused refresh token must be refused: %d", w.Code)
	}
	if mcpWith(h, tok2["access_token"].(string), `{"jsonrpc":"2.0","id":1,"method":"ping"}`).Code != 401 {
		t.Error("a leaked-refresh detection should have ended the whole connection")
	}
	// A code is exchanged once: exchanging it again is a replay, refused as
	// an invalid grant. (Concurrent exchanges are the identity owner's:
	// TestCodeExchangeConcurrentClaim.)
	code, verifier := obtainCode(t, h, admin, clientID)
	if w := exchange(h, clientID, code, verifier); w.Code != 200 {
		t.Fatalf("first exchange: %d %s", w.Code, w.Body.String())
	}
	if w := exchange(h, clientID, code, verifier); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Fatalf("a replayed code must be refused as an invalid grant: %d %s", w.Code, w.Body.String())
	}
}

func TestOAuthPKCEAndRevocation(t *testing.T) {
	t.Parallel()
	_, h, admin := oauthFixture(t)
	clientID := registerClient(t, h)
	_, challenge := pkce()
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:9999/callback"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	prepareConsent(t, h, admin, q)
	q.Set("decision", "allow")
	req := httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(admin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	loc, _ := url.Parse(w.Header().Get("Location"))
	code := loc.Query().Get("code")
	// Wrong verifier → invalid_grant.
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://127.0.0.1:9999/callback"},
		"client_id": {clientID}, "code_verifier": {"wrong-wrong-wrong-wrong-wrong-wrong-wrong-wrong"}}
	req = httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "PKCE") {
		t.Fatalf("bad verifier: %d %s", w.Code, w.Body.String())
	}
	// A denied consent redirects with access_denied.
	prepareConsent(t, h, admin, q)
	q.Set("decision", "deny")
	req = httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(admin)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !strings.Contains(w.Header().Get("Location"), "error=access_denied") {
		t.Errorf("deny: %s", w.Header().Get("Location"))
	}
	// A real connection shows under the person's connected agents, and
	// disconnecting it kills the token immediately.
	tok := authorizeAndExchange(t, h, admin, clientID, "forge:user:read")
	accessToken := tok["access_token"].(string)
	req = httptest.NewRequest("GET", "/api/v1/auth/delegations", nil)
	req.AddCookie(admin)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var list struct {
		Delegations []identity.Delegation `json:"delegations"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Delegations) != 1 || list.Delegations[0].ClientName != "Claude Code" {
		t.Fatalf("delegations: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("DELETE", "/api/v1/auth/delegations/"+list.Delegations[0].ID.String(), nil)
	req.AddCookie(admin)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body.String())
	}
	if mcpWith(h, accessToken, `{"jsonrpc":"2.0","id":1,"method":"ping"}`).Code != 401 {
		t.Error("a disconnected agent's token must stop working at once")
	}
}

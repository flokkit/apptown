package forge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func consentRequest(clientID string) url.Values {
	_, challenge := pkce()
	return url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:9999/callback"},
		"scope": {"apptown:user:read"}, "state": {"original-state"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
}

func postConsent(h http.Handler, q url.Values, who *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(q.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if who != nil {
		r.AddCookie(who)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOAuthConsentBoundToSessionAndRequest(t *testing.T) {
	t.Parallel()
	a, h, who := oauthFixture(t)
	client := registerClient(t, h)
	q := consentRequest(client)
	q.Set("decision", "allow")
	if w := postConsent(h, q, who); w.Code != 403 {
		t.Fatalf("direct consent POST accepted: %d", w.Code)
	}
	prepareConsent(t, h, who, q)
	otherSession := sessionFrom(t, postJSON(h, "/api/v1/auth/login", map[string]string{"username": "boss", "password": "boss-password"}))
	if w := postConsent(h, q, otherSession); w.Code != 403 {
		t.Fatalf("same user's different session accepted: %d", w.Code)
	}
	for field, value := range map[string]string{
		"scope": "apptown:change:create", "state": "changed-state", "code_challenge": "attacker-challenge",
		"resource": "https://other.test/mcp/user", "client_id": registerClient(t, h),
	} {
		changed, _ := url.ParseQuery(q.Encode())
		changed.Set(field, value)
		w := postConsent(h, changed, who)
		if w.Code == 302 && !strings.Contains(w.Header().Get("Location"), "error=") {
			t.Fatalf("changed %s obtained a code: %v", field, w.Header())
		}
	}
	var codes int
	// sql-guarantee: codes are readable only by hash; a apptownd consent must mint no code at all, which only a store-wide count shows
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM oauth_codes`).Scan(&codes); err != nil || codes != 0 {
		t.Fatalf("apptownd consent minted a code: %d %v", codes, err)
	}
	if w := postConsent(h, q, who); w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "code=") {
		t.Fatalf("original request no longer works: %d %s", w.Code, w.Body.String())
	}
	if w := postConsent(h, q, who); w.Code != 403 {
		t.Fatalf("consent replay accepted: %d", w.Code)
	}
}

func TestOAuthConsentExpiryDenialAndConcurrentUse(t *testing.T) {
	t.Parallel()
	a, h, who := oauthFixture(t)
	q := consentRequest(registerClient(t, h))
	prepareConsent(t, h, who, q)
	q.Set("decision", "allow")
	// sql-guarantee: Expire the browser-prepared consent before the HTTP decision; normal consumption deletes it and cannot test expiry refusal on the same token.
	if _, err := a.DB.Exec(`UPDATE oauth_consents SET expires_at=CURRENT_TIMESTAMP - INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	if w := postConsent(h, q, who); w.Code != 403 {
		t.Fatalf("expired consent accepted: %d", w.Code)
	}
	prepareConsent(t, h, who, q)
	q.Set("decision", "deny")
	if w := postConsent(h, q, who); w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "access_denied") {
		t.Fatalf("denial: %d %v", w.Code, w.Header())
	}
	q.Set("decision", "allow")
	if w := postConsent(h, q, who); w.Code != 403 {
		t.Fatalf("denied consent reusable for allow: %d", w.Code)
	}
	prepareConsent(t, h, who, q)
	wins := 0
	for _, status := range race(6, func(int) int { return postConsent(h, q, who).Code }) {
		switch status {
		case 302:
			wins++
		case 403:
		default:
			t.Fatalf("unexpected concurrent consent status: %d", status)
		}
	}
	if wins != 1 {
		t.Fatalf("want exactly one consent, got %d", wins)
	}
	a.sweepExpiredCredentials(context.Background())
	var pending int
	// sql-guarantee: the sweep must delete expired and consumed consents; no owner read lists consents
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM oauth_consents`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("expired/consumed consent retained: %d %v", pending, err)
	}
}

func TestOAuthProxyConsentBoundToBrowserAndPerson(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "trusted-header", AuthHeader: "X-Test-User", AuthProxySecret: "test-proxy-secret"})
	a.SeedAdministrators(context.Background(), []string{"boss"})
	h := a.Handler()
	q := consentRequest(registerClient(t, h))
	call := func(method string, who string, cookie *http.Cookie) *httptest.ResponseRecorder {
		path := "/oauth/authorize"
		if method == "GET" {
			path += "?" + q.Encode()
		}
		r := httptest.NewRequest(method, path, strings.NewReader(q.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-AppTown-Proxy-Secret", "test-proxy-secret")
		r.Header.Set("X-Test-User", who)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "boss", nil)
	q.Set("consent_token", consentToken(t, w))
	q.Set("decision", "allow")
	if len(w.Result().Cookies()) != 1 {
		t.Fatal("proxy consent must establish browser binding")
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.Domain != "" || cookie.Path != "/" {
		t.Fatal("proxy binding cookie must be private and host-only")
	}
	if w := call("POST", "boss", nil); w.Code != 403 {
		t.Fatalf("proxy user without browser binding accepted: %d", w.Code)
	}
	if w := call("POST", "someone-else", cookie); w.Code != 403 {
		t.Fatalf("different proxy user accepted: %d", w.Code)
	}
	if w := call("POST", "boss", cookie); w.Code != 302 {
		t.Fatalf("legitimate proxy consent failed: %d %s", w.Code, w.Body.String())
	}
}

func TestOAuthConsentStorageErrors(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"INSERT INTO oauth_consents", "-- name: ConsumeConsent", "INSERT INTO oauth_codes"} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			a, h, who := oauthFixture(t)
			client := registerClient(t, h)
			_, challenge := pkce()
			q := url.Values{"client_id": {client}, "redirect_uri": {"http://127.0.0.1:9999/callback"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
			method, path := "GET", "/oauth/authorize?"+q.Encode()
			var body io.Reader
			if marker != "INSERT INTO oauth_consents" {
				prepareConsent(t, h, who, q)
				q.Set("decision", "allow")
				method, path, body = "POST", "/oauth/authorize", strings.NewReader(q.Encode())
			}
			restore := a.DB.FailNextStatement(marker)
			defer restore()
			req := httptest.NewRequest(method, path, body)
			req.AddCookie(who)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 500 || w.Header().Get("Location") != "" {
				t.Fatalf("storage failure redirected or succeeded: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

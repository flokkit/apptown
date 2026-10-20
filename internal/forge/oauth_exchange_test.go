package forge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/stamp"
)

// obtainCode walks consent and returns the authorization code and verifier
// without exchanging it.
func obtainCode(t *testing.T, h http.Handler, who *http.Cookie, clientID string) (code, verifier string) {
	t.Helper()
	verifier, challenge := pkce()
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:9999/callback"},
		"scope": {"apptown:user:read"}, "state": {"xyz"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "decision": {"allow"}}
	prepareConsent(t, h, who, q)
	req := httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(q.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(who)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 302 {
		t.Fatalf("consent: %d %s", w.Code, w.Body.String())
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	return loc.Query().Get("code"), verifier
}

func exchange(h http.Handler, clientID, code, verifier string) *httptest.ResponseRecorder {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://127.0.0.1:9999/callback"},
		"client_id": {clientID}, "code_verifier": {verifier}}
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestOAuthRefreshRotatedOnceUnderRace(t *testing.T) {
	t.Parallel()
	a, h, who := oauthFixture(t)
	clientID := registerClient(t, h)
	tok := authorizeAndExchange(t, h, who, clientID, "apptown:user:read")
	rt := tok["refresh_token"].(string)
	refresh := func() *httptest.ResponseRecorder {
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {clientID}}
		req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	codes := race(5, func(int) int { return refresh().Code })
	if count(codes, 200) != 1 {
		t.Fatalf("exactly one rotation may succeed: %v", codes)
	}
	// A reuse is a leak by design: the delegation has been ended.
	var revoked stamp.Time
	// sql-guarantee: no owner read returns an ended delegation's revoked_at; the owner lists only live delegations
	_ = a.DB.QueryRow(`SELECT revoked_at FROM delegations ORDER BY id DESC LIMIT 1`).Scan(&revoked)
	if !revoked.Valid {
		t.Fatal("a raced refresh is a reuse; the connection must be ended")
	}
}

func TestOAuthExchangeRollsBackWhole(t *testing.T) {
	t.Parallel()
	a, h, who := oauthFixture(t)
	clientID := registerClient(t, h)
	code, verifier := obtainCode(t, h, who, clientID)
	dbtest.FailNextStatement(t, a.DB, "INSERT INTO delegations")
	if w := exchange(h, clientID, code, verifier); w.Code != 500 {
		t.Fatalf("the injected failure must surface: %d %s", w.Code, w.Body.String())
	}
	// Nothing partial: the code is still unused, and the client may retry.
	w := exchange(h, clientID, code, verifier)
	if w.Code != 200 {
		t.Fatalf("after a rolled-back exchange the code must still work: %d %s", w.Code, w.Body.String())
	}
	var tok map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	if tok["access_token"] == nil {
		t.Fatal("no tokens after the retry")
	}
}

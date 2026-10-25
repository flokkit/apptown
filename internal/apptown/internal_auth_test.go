package apptown

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func internalApp(t *testing.T, configs ...Config) (*App, http.Handler) {
	t.Helper()
	cfg := Config{}
	if len(configs) != 0 {
		cfg = configs[0]
	}
	cfg.AuthMode = "internal"
	a := testApp(t, cfg)

	return a, a.Handler() // Handler() already wraps AuthMiddleware
}

// firstAccount is the body that makes the first account: the name, the
// password and the setup token AppTown keeps on its host.
func firstAccount(t *testing.T, a *App, username, password string) map[string]string {
	t.Helper()
	token, err := a.Access.EnsureSetupToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"username": username, "password": password, "setupToken": token}
}

func postJSON(h http.Handler, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("X-AppTown-Actor", "bls")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestInternalModeLocksTheDoor(t *testing.T) {
	t.Parallel()
	_, h := internalApp(t)

	// The actor header — open mode's honesty system — is dead here.
	req := httptest.NewRequest("GET", "/api/v1/projects", nil)
	req.Header.Set("X-AppTown-Actor", "impostor")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("actor header must not authenticate in internal mode: got %d", w.Code)
	}

	// The login door itself stays reachable.
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"needsBootstrap":true`) {
		t.Fatalf("auth/me should be open and report bootstrap: %d %s", w.Code, w.Body.String())
	}
}

func TestBootstrapThenLogin(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)

	if w := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "bls", "short")); w.Code != 400 {
		t.Fatalf("8-char password floor not enforced: %d", w.Code)
	}
	w := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "bls", "correct-horse"))
	if w.Code != 200 {
		t.Fatalf("bootstrap failed: %d %s", w.Code, w.Body.String())
	}
	cookie := sessionFrom(t, w)

	// Bootstrap is once: the second caller is told to sign in.
	if w := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "eve", "evil-password")); w.Code != 409 {
		t.Fatalf("bootstrap must refuse once an account exists: %d", w.Code)
	}

	// The session cookie authenticates and carries the right identity.
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user":"bls"`) ||
		!strings.Contains(rec.Body.String(), `"role":"admin"`) {
		t.Fatalf("session identity wrong: %d %s", rec.Code, rec.Body.String())
	}

	// Wrong password and unknown user get the same answer.
	bad1 := postJSON(h, "/api/v1/auth/login", map[string]string{"username": "bls", "password": "wrong-password"})
	bad2 := postJSON(h, "/api/v1/auth/login", map[string]string{"username": "ghost", "password": "wrong-password"})
	if bad1.Code != 401 || bad2.Code != 401 || bad1.Body.String() != bad2.Body.String() {
		t.Fatalf("login errors must not distinguish user-exists: %d/%d %q vs %q",
			bad1.Code, bad2.Code, bad1.Body.String(), bad2.Body.String())
	}
	if w := postJSON(h, "/api/v1/auth/login", map[string]string{"username": "bls", "password": "correct-horse"}); w.Code != 200 {
		t.Fatalf("real login failed: %d %s", w.Code, w.Body.String())
	}

	// Logout kills the session.
	if w := postJSON(h, "/api/v1/auth/logout", nil, cookie); w.Code != 200 {
		t.Fatalf("logout: %d", w.Code)
	}
	req = httptest.NewRequest("GET", "/api/v1/me", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("session must die on logout: got %d", rec.Code)
	}
}

func TestPasswordResetRevokesSessions(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	w := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "bls", "correct-horse"))
	admin := sessionFrom(t, w)

	// Admin creates a member; member signs in.
	if w := postJSON(h, "/api/v1/users", map[string]string{"username": "kid", "password": "kid-password"}, admin); w.Code != 200 {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	kid := sessionFrom(t, postJSON(h, "/api/v1/auth/login", map[string]string{"username": "kid", "password": "kid-password"}))

	// Reset the kid's password: their old session must be out.
	if w := postJSON(h, "/api/v1/users", map[string]string{"username": "kid", "password": "new-kid-password"}, admin); w.Code != 200 {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.AddCookie(kid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("old session survived a password reset: %d", rec.Code)
	}

	// Members cannot manage users.
	kid2 := sessionFrom(t, postJSON(h, "/api/v1/auth/login", map[string]string{"username": "kid", "password": "new-kid-password"}))
	if w := postJSON(h, "/api/v1/users", map[string]string{"username": "sneak", "password": "sneak-password"}, kid2); w.Code != 403 {
		t.Fatalf("member managed users: %d", w.Code)
	}
	_ = a
}

func sessionFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie in response: %d %s", w.Code, w.Body.String())
	return nil
}

// The franchise, minimally (permissions.go): AppTown is not a democracy —
// amend/release are privileges, granted by name. An unclaimed privilege
// belongs to every member; the first grant closes it; admins hold all.
func TestPrivilegesRequireExplicitCapabilityGrants(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "boss")
	if _, err := a.Access.EnsureUser(context.Background(), "ana"); err != nil {
		t.Fatal(err)
	}
	if err := a.Access.SaveGroup(context.Background(), identity.GroupChange{Name: "leads", Members: []string{"ana"}}, access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}
	if privileged(t, a, "anyone", "member", "amend") || privileged(t, a, "anyone", "admin", "amend") {
		t.Fatal("unclaimed permission or role label granted authority")
	}
	a.addGrant("group", "leads", access.RoleStandardsAdmin, "", access.ScopeInstance, "", "fixture", "fixture", "")
	if !privileged(t, a, "ana", "member", "amend") || privileged(t, a, "outsider", "member", "amend") {
		t.Fatal("explicit group capability scope not respected")
	}
	if privileged(t, a, "ana", "member", "release") {
		t.Fatal("standards authority granted release access")
	}
	a.addGrant("user", "ana", access.RoleReleaseManager, "", access.ScopeTarget, "*", "fixture", "fixture", "")
	if !privileged(t, a, "ana", "member", "release") {
		t.Fatal("explicit release grant ignored")
	}
}

func TestAmendGateOnWorkflowSave(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	admin := sessionFrom(t, postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "bls", "correct-horse")))
	if w := postJSON(h, "/api/v1/users", map[string]string{"username": "kid", "password": "kid-password"}, admin); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	kid := sessionFrom(t, postJSON(h, "/api/v1/auth/login", map[string]string{"username": "kid", "password": "kid-password"}))
	wf := map[string]string{"yaml": `
apiVersion: apptown.dev/v1
kind: Workflow
metadata: { name: gates }
spec:
  kind: lifecycle
  stages:
    - name: work
      steps:
        - name: ok
          uses: approval
          with: { approvers: [bls], means: "I read it" }
`}
	// Every write needs its capability; unrelated standards authority cannot
	// edit executable workflows.
	if w := postJSON(h, "/api/v1/workflows/library", wf, kid); w.Code != 403 {
		t.Fatalf("ungranted workflow edit: %d", w.Code)
	}
	grant := map[string]any{"subjectType": "user", "subject": "kid", "role": "standards-administrator", "scope": "instance", "scopeId": ""}
	if w := postJSON(h, "/api/v1/authz/grants", grant, admin); w.Code != 201 {
		t.Fatalf("standards grant: %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(h, "/api/v1/workflows/library", wf, kid); w.Code != 403 {
		t.Fatalf("standards authority edited workflows: %d", w.Code)
	}
	grant["role"] = "execution-administrator"
	if w := postJSON(h, "/api/v1/authz/grants", grant, kid); w.Code != 403 {
		t.Fatalf("self grant: %d", w.Code)
	}
	if w := postJSON(h, "/api/v1/authz/grants", grant, admin); w.Code != 201 {
		t.Fatalf("execution grant: %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(h, "/api/v1/workflows/library", wf, kid); w.Code != 200 && w.Code != 201 {
		t.Fatalf("explicit execution authority: %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(h, "/api/v1/workflows/library", wf, admin); w.Code != 200 && w.Code != 201 {
		t.Fatalf("administrator: %d %s", w.Code, w.Body.String())
	}

}

// Console containers hold a session token, not a cookie: internal mode
// must let them through with the session's actor as identity — chat keeps
// working the moment the instance flips.
func TestConsoleTokenWorksInInternalMode(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	m := consoleMetaFor("s1", ids.Nil, ids.Nil)
	m.Actor, m.Token = "pi-console", "sekret-token"
	// A chat acts for an account; one with none is refused like a disabled one.
	testAccounts(t, a.DB, "pi-console")
	a.Console.Restore(m)

	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("X-AppTown-Session-Token", "sekret-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"pi-console via console/s1"`) {
		t.Fatalf("console token must authenticate with provenance identity: %d %s", w.Code, w.Body.String())
	}
	var minted int
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE username LIKE '%via console%'`).Scan(&minted)
	if minted != 0 {
		t.Fatal("a console must not mint a users row — it is an instrument, not a person")
	}
	req = httptest.NewRequest("GET", "/api/v1/projects", nil)
	req.Header.Set("X-AppTown-Session-Token", "wrong")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("bad console token must not authenticate: %d", w.Code)
	}
}

// Membership requires explicit roles; a group grants only to its members.
// groups count; console provenance inherits the person's membership;
// admins always pass. Reading is never gated.
func TestProjectRosterGate(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "boss")
	_, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{ID: idstest.N(1), Slug: "game", Name: "Game"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Access.EnsureUser(context.Background(), "dad"); err != nil {
		t.Fatal(err)
	}
	if err := a.Access.SaveGroup(context.Background(), identity.GroupChange{Name: "parents", Members: []string{"dad"}}, access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "session"}); err != nil {
		t.Fatal(err)
	}

	if a.projectMember(idstest.N(1), "kid-a", "member") || a.projectMember(idstest.N(1), "anyone", "admin") {
		t.Fatal("an empty roster or role label granted authority")
	}
	a.addGrant("user", "kid-b", access.RoleProjectMaintainer, "", access.ScopeProject, idstest.N(1).String(), "fixture", "fixture", "")
	a.addGrant("group", "parents", access.RoleProjectMaintainer, "", access.ScopeProject, idstest.N(1).String(), "fixture", "fixture", "")
	if a.projectMember(idstest.N(1), "kid-a", "member") {
		t.Fatal("outside member accepted")
	}
	if !a.projectMember(idstest.N(1), "kid-b", "member") || !a.projectMember(idstest.N(1), "dad", "member") {
		t.Fatal("explicit member or group role rejected")
	}

	// HTTP: kid-a cannot start a change; kid-b can; reads stay open.

	h := a.Handler()
	req := httptest.NewRequest("POST", "/api/v1/projects/game/changes", strings.NewReader(`{"title":"pls"}`))
	req.Header.Set("X-AppTown-Actor", "kid-a")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "may not open a change on this project") {
		t.Fatalf("non-member starting work must 403 in plain words: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/v1/projects/game", nil)
	req.Header.Set("X-AppTown-Actor", "kid-a")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("reading must stay open: %d", w.Code)
	}
}

func TestBootstrapSessionFailureSendsNoCookieAndAllowsRetry(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	in := firstAccount(t, a, "boss", "first-password")
	restore := a.DB.FailNextStatement("-- name: InsertSession")
	w := postJSON(h, "/api/v1/auth/bootstrap", in)
	restore()
	if w.Code != 500 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("failed bootstrap reported success or sent a cookie: %d", w.Code)
	}
	w = postJSON(h, "/api/v1/auth/bootstrap", in)
	if w.Code != 200 {
		t.Fatalf("failed bootstrap was not retryable: %d %s", w.Code, w.Body)
	}
	cookie := sessionFrom(t, w)
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user":"boss"`) {
		t.Fatalf("retry's cookie was not usable: %d", rec.Code)
	}
}

// trusted-header mode: the proxy secret is the whole trust; without it the
// header is just a header.
func TestTrustedHeaderMode(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "trusted-header", AuthHeader: "X-SSO-User", AuthProxySecret: "hush"})

	h := a.Handler()
	try := func(secret, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/me", nil)
		if secret != "" {
			req.Header.Set("X-AppTown-Proxy-Secret", secret)
		}
		if user != "" {
			req.Header.Set("X-SSO-User", user)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := try("", "mallory"); w.Code != 401 {
		t.Fatalf("header without secret must not authenticate: %d", w.Code)
	}
	if w := try("wrong", "mallory"); w.Code != 401 {
		t.Fatalf("wrong secret must not authenticate: %d", w.Code)
	}
	if w := try("hush", "alice"); w.Code != 200 || !strings.Contains(w.Body.String(), `"alice"`) {
		t.Fatalf("proxy identity: %d %s", w.Code, w.Body.String())
	}
	if w := try("hush", ""); w.Code != 401 {
		t.Fatalf("secret without a username is still nobody: %d", w.Code)
	}
	// A name AppTown would read as something else is refused at the door.
	for _, name := range []string{"alice via console/x", "run/3", "system"} {
		if w := try("hush", name); w.Code != 401 || strings.Contains(w.Body.String(), `"user"`) {
			t.Fatalf("proxy name %q: %d %s", name, w.Code, w.Body.String())
		}
	}
}

// Open mode takes the actor header as the name; one AppTown would read as
// something else is refused, not recorded.
func TestOpenModeRefusesAnUnusableActor(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	h := a.Handler()
	for name, want := range map[string]int{"alice": 200, "alice via console/x": 401, "chat/9": 401} {
		req := httptest.NewRequest("GET", "/api/v1/me", nil)
		req.Header.Set("X-AppTown-Actor", name)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("open-mode actor %q: %d %s", name, w.Code, w.Body.String())
		}
	}
}

// Bearer tokens: valid ones authenticate in any mode; revoked ones are dead.
func TestBearerTokenLifecycle(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	testAdministrator(t, a, "boss")
	if _, err := a.Access.EnsureUser(context.Background(), "cli-bls"); err != nil {
		t.Fatal(err)
	}
	issued, err := a.Access.IssueToken(context.Background(), identity.NewToken{Purpose: "cli", Name: "cli", User: "cli-bls", Role: "admin"}, access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Plaintext)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cli-bls"`) {
		t.Fatalf("bearer auth: %d %s", w.Code, w.Body.String())
	}
	if revoked, err := a.Access.RevokeToken(context.Background(), "cli", access.Principal{ID: testPrincipalOf(t, a.DB, "boss"), Kind: "user", Name: "boss", Via: "session"}); err != nil || !revoked {
		t.Fatalf("revoke token: revoked=%v err=%v", revoked, err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("revoked token must be dead: %d", w.Code)
	}
	req.Header.Set("Authorization", "Bearer never-was-a-token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unknown token must 401: %d", w.Code)
	}
}

// Sessions expire: a cookie past its expiry is deleted and refused.
func TestSessionExpiry(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	cookie := sessionFrom(t, postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "boss-password")))
	// sql-guarantee: Age an issued HTTP session without deleting it, so middleware must detect expiry; Identity issues with the current clock and has no expiry mutation.
	if _, err := a.DB.Exec(`UPDATE sessions SET expires_at='2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("expired session must 401: %d", w.Code)
	}
	var n int
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Fatalf("expired session should be reaped, %d remain", n)
	}
}

// An administrator's invite link lets the invitee, who has no session, see
// whose account it is, set their own password and be signed in; the link
// works once, and a member cannot make one.
func TestInviteLinkSignsTheInviteeIn(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	admin := sessionFrom(t, postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "bls", "correct-horse")))
	w := postJSON(h, "/api/v1/invites", map[string]any{"username": "kim", "label": "Kim Lee"}, admin)
	if w.Code != 201 {
		t.Fatalf("create invite: %d %s", w.Code, w.Body.String())
	}
	var made struct{ Link string }
	if err := json.Unmarshal(w.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(made.Link, "http://example.com/invite/"+identity.InvitePrefix) {
		t.Fatalf("invite link: %q", made.Link)
	}
	token := strings.TrimPrefix(made.Link, "http://example.com/invite/")

	shown := postJSON(h, "/api/v1/auth/invite", map[string]string{"token": token})
	if shown.Code != 200 || !strings.Contains(shown.Body.String(), `"username":"kim"`) || !strings.Contains(shown.Body.String(), `"invitedBy":"bls"`) {
		t.Fatalf("invite view without a session: %d %s", shown.Code, shown.Body.String())
	}
	accepted := postJSON(h, "/api/v1/auth/invite/accept", map[string]string{"token": token, "password": "kims-password"})
	kim := sessionFrom(t, accepted)
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.AddCookie(kim)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"user":"kim"`) {
		t.Fatalf("invitee session: %d %s", rec.Code, rec.Body.String())
	}
	if w := postJSON(h, "/api/v1/auth/invite/accept", map[string]string{"token": token, "password": "other-password"}); w.Code != 409 {
		t.Fatalf("second use: %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(h, "/api/v1/invites", map[string]any{"username": "friend"}, kim); w.Code != 403 {
		t.Fatalf("a member made an invite: %d %s", w.Code, w.Body.String())
	}
}

// A password reset link, made by an administrator for an account that has a
// password, opens on the same invite page: its holder, with no session, sees
// it is a password reset, sets a new password and is signed in, and the
// account's earlier sessions stop working. A member cannot make one.
func TestPasswordLinkSignsTheHolderInAndEndsOldSessions(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	admin := sessionFrom(t, postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "bls", "correct-horse")))
	if w := postJSON(h, "/api/v1/users", map[string]any{"username": "kim", "password": "kims-old-password"}, admin); w.Code != 200 {
		t.Fatalf("add kim: %d %s", w.Code, w.Body.String())
	}
	oldKim := sessionFrom(t, postJSON(h, "/api/v1/auth/login", map[string]string{"username": "kim", "password": "kims-old-password"}))
	if w := postJSON(h, "/api/v1/password-links", map[string]any{"username": "kim"}, oldKim); w.Code != 403 {
		t.Fatalf("a member made a password reset link: %d %s", w.Code, w.Body.String())
	}
	w := postJSON(h, "/api/v1/password-links", map[string]any{"username": "kim", "days": 2}, admin)
	if w.Code != 201 {
		t.Fatalf("create password reset link: %d %s", w.Code, w.Body.String())
	}
	var made struct{ Link string }
	if err := json.Unmarshal(w.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(made.Link, "http://example.com/invite/")
	if !strings.HasPrefix(token, identity.InvitePrefix) {
		t.Fatalf("password reset link: %q", made.Link)
	}
	shown := postJSON(h, "/api/v1/auth/invite", map[string]string{"token": token})
	if shown.Code != 200 || !strings.Contains(shown.Body.String(), `"purpose":"password"`) || !strings.Contains(shown.Body.String(), `"username":"kim"`) {
		t.Fatalf("link view without a session: %d %s", shown.Code, shown.Body.String())
	}
	kim := sessionFrom(t, postJSON(h, "/api/v1/auth/invite/accept", map[string]string{"token": token, "password": "kims-new-password"}))
	me := func(c *http.Cookie) int {
		req := httptest.NewRequest("GET", "/api/v1/me", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := me(kim); got != 200 {
		t.Fatalf("new session: %d", got)
	}
	if got := me(oldKim); got != 401 {
		t.Fatalf("kim's old session still works after the reset: %d", got)
	}
	if w := postJSON(h, "/api/v1/auth/login", map[string]string{"username": "kim", "password": "kims-old-password"}); w.Code == 200 {
		t.Fatal("kim's old password still works after the reset")
	}
}

// Under single sign-on AppTown holds no passwords to reset or set, so an
// administrator asking for a password reset link or an invite is told where
// that is done instead.
func TestLinksOutsideInternalSignInSayWhereToGo(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{AuthMode: "trusted-header", AuthHeader: "X-SSO-User", AuthProxySecret: "hush"})
	testAdministrator(t, a, "boss")
	h := a.Handler()
	for path, want := range map[string]string{
		"/api/v1/password-links": "Password reset links need AppTown's own sign-in (--auth internal); with single sign-on, reset the password in your identity provider.",
		"/api/v1/invites":        "Invites need AppTown's own sign-in (--auth internal); with single sign-on, add people in your identity provider.",
	} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"username":"kim"}`))
		req.Header.Set("X-AppTown-Proxy-Secret", "hush")
		req.Header.Set("X-SSO-User", "boss")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var body struct{ Error string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 404 || body.Error != want {
			t.Fatalf("%s under single sign-on: %d %s", path, w.Code, w.Body.String())
		}
	}
}

// Every consumer of AppTown's own address agrees on it, under each way
// -public-host can be given: a browser at that origin is let in (the browser
// origin check), signs in with a cookie that is Secure exactly when the
// origin is https, gets invite and password reset links on that origin, and
// an agent is sent there by OAuth discovery. The guides' live links name it
// only when it was stated whole. A proxy's X-Forwarded-Proto never decides
// it; an AppTown behind a TLS proxy states its origin.
func TestAppTownAddressConsumersAgree(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		cfg       Config
		forwarded string // what a proxy in front says; it must not matter
		tls       bool   // the request reached AppTown over TLS itself
		origin    string // the address everyone must name
		docs      string // the guides' live link, "" for words
	}{
		{"stated https behind a TLS proxy, no tiers", Config{PublicHost: "https://forge.example"}, "https", false, "https://forge.example", "https://forge.example"},
		{"stated https, http tiers", Config{PublicHost: "https://forge.example", TierDomains: []string{"preview=http://p.example"}}, "", false, "https://forge.example", "https://forge.example"},
		{"stated http, proxy says https", Config{PublicHost: "http://forge.example"}, "https", false, "http://forge.example", "http://forge.example"},
		{"bare name, https tiers (live)", Config{PublicHost: "forge.example", TierDomains: []string{"preview=p.example"}}, "", false, "https://forge.example", ""},
		{"bare name, direct TLS", Config{PublicHost: "forge.example"}, "", true, "https://forge.example", ""},
		{"bare name, plain, proxy says https", Config{PublicHost: "forge.example"}, "https", false, "http://forge.example", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, h := internalApp(t, tc.cfg)
			secure := strings.HasPrefix(tc.origin, "https://")
			send := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
				var b []byte
				if body != nil {
					b, _ = json.Marshal(body)
				}
				target := "http://forge.example" + path
				if tc.tls {
					target = "https://forge.example" + path
				}
				req := httptest.NewRequest(method, target, bytes.NewReader(b))
				req.Host = "forge.example"
				req.RemoteAddr = "127.0.0.1:40000"
				req.Header.Set("Content-Type", "application/json")
				if method == "POST" {
					req.Header.Set("Origin", tc.origin)
					req.Header.Set("Sec-Fetch-Site", "same-origin")
				}
				if tc.forwarded != "" {
					req.Header.Set("X-Forwarded-Proto", tc.forwarded)
				}
				if cookie != nil {
					req.AddCookie(cookie)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				return w
			}
			// Browser origin check and session cookie.
			w := send("POST", "/api/v1/auth/bootstrap", firstAccount(t, a, "boss", "boss-password"), nil)
			if w.Code/100 != 2 {
				t.Fatalf("a browser at %s was refused: %d %s", tc.origin, w.Code, w.Body)
			}
			var session *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Value != "" {
					session = c
				}
			}
			wantName := map[bool]string{true: secureSessionCookie, false: sessionCookie}[secure]
			if session == nil || session.Secure != secure || session.Name != wantName {
				t.Fatalf("session cookie %+v, want %s Secure=%v", session, wantName, secure)
			}
			// Invite and password reset links.
			for path, body := range map[string]any{
				"/api/v1/invites":        map[string]any{"username": "kim"},
				"/api/v1/password-links": map[string]any{"username": "boss"},
			} {
				w := send("POST", path, body, session)
				var made struct{ Link string }
				if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &made) != nil || !strings.HasPrefix(made.Link, tc.origin+"/invite/") {
					t.Errorf("%s: %d %s, want a link on %s", path, w.Code, w.Body, tc.origin)
				}
			}
			// OAuth discovery.
			var meta struct {
				Issuer        string `json:"issuer"`
				TokenEndpoint string `json:"token_endpoint"`
			}
			w = send("GET", "/.well-known/oauth-authorization-server", nil, nil)
			if json.Unmarshal(w.Body.Bytes(), &meta) != nil || meta.Issuer != tc.origin || meta.TokenEndpoint != tc.origin+"/oauth/token" {
				t.Errorf("discovery: %d %s, want %s", w.Code, w.Body, tc.origin)
			}
			// The guides' live links.
			var guide httpapi.DocGuide
			w = send("GET", "/api/v1/docs/guides/quickstart", nil, session)
			if json.Unmarshal(w.Body.Bytes(), &guide) != nil || guide.Links.Origin.URL != tc.docs {
				t.Errorf("docs origin %q, want %q (%d)", guide.Links.Origin.URL, tc.docs, w.Code)
			}
		})
	}
}

// The first account of an internal-auth AppTown needs the setup token the
// AppTown writes on its own host: without it the first visitor to the login
// page would become the administrator. A missing or wrong token is refused
// and names where the token is; the right one makes the administrator and
// the token is spent; with an account there is no token to make.
//
// Kills: a bootstrap any caller could make while no account existed.
func TestTheFirstAccountNeedsTheSetupToken(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	path := filepath.Join(a.cfg.dataDir, "setup-token")
	token, err := a.Access.EnsureSetupToken(t.Context())
	if err != nil || len(token) < 32 {
		t.Fatalf("setup token: %q %v", token, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the token is readable by the host's operator only: %v %v", st, err)
	}
	if again, _ := a.Access.EnsureSetupToken(t.Context()); again != token {
		t.Fatal("a restart replaced a token already handed out")
	}
	for name, body := range map[string]map[string]string{
		"no token":    {"username": "eve", "password": "evil-password"},
		"wrong token": {"username": "eve", "password": "evil-password", "setupToken": "guess"},
	} {
		if w := postJSON(h, "/api/v1/auth/bootstrap", body); w.Code != 403 || !strings.Contains(w.Body.String(), path) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := postJSON(h, "/api/v1/auth/bootstrap", map[string]string{"username": "bls", "password": "correct-horse", "setupToken": token}); w.Code != 200 {
		t.Fatalf("the operator's first account: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the token outlived the first account: %v", err)
	}
	if w := postJSON(h, "/api/v1/auth/bootstrap", map[string]string{"username": "eve", "password": "evil-password", "setupToken": token}); w.Code != 409 {
		t.Fatalf("a second first account: %d", w.Code)
	}
	if again, err := a.Access.EnsureSetupToken(t.Context()); err != nil || again != "" {
		t.Fatalf("an AppTown with an account made a token: %q %v", again, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an AppTown with an account wrote a token file")
	}
}

// Setup is complete when the setup marker says so, not when an account
// exists: a name recorded while AppTown ran in open mode leaves setup
// open, so a token is made and the first account can still be created. A
// seeded administrator completes setup; a token left from before is removed.
//
// Kills: a token made only "while no account exists", which left an AppTown
// switched from open to internal mode with no way to make its first account.
func TestTheSetupTokenFollowsTheSetupMarker(t *testing.T) {
	t.Parallel()
	a, h := internalApp(t)
	if _, err := a.Access.EnsureUser(t.Context(), "visitor"); err != nil {
		t.Fatal(err)
	}
	if w := postJSON(h, "/api/v1/auth/bootstrap", firstAccount(t, a, "bls", "correct-horse")); w.Code != 200 {
		t.Fatalf("a recorded visitor blocked the first account: %d %s", w.Code, w.Body.String())
	}

	seeded, _ := internalApp(t)
	path := filepath.Join(seeded.cfg.dataDir, "setup-token")
	if token, err := seeded.Access.EnsureSetupToken(t.Context()); err != nil || token == "" {
		t.Fatalf("no token before seeding: %q %v", token, err)
	}
	if err := seeded.Access.SeedAdministrator(t.Context(), "boss"); err != nil {
		t.Fatal(err)
	}
	if token, err := seeded.Access.EnsureSetupToken(t.Context()); err != nil || token != "" {
		t.Fatalf("a token after a seeded administrator: %q %v", token, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the token from before seeding was left behind: %v", err)
	}
}

// A guess at a door that takes a secret costs time: a client gets a burst
// of attempts, then one every few seconds, and another client's budget is
// its own. The doors share one budget per client.
//
// Kills: credential doors with no limit, which let a client try passwords,
// invite tokens or the setup token as fast as it could send them.
func TestCredentialDoorsSlowAClientsGuesses(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_800_000_000, 0)
	l := newCredentialLimiter(func() time.Time { return now })
	for i := 0; i < credentialBurst; i++ {
		if ok, _ := l.take("10.0.0.1"); !ok {
			t.Fatalf("attempt %d of the burst refused", i+1)
		}
	}
	ok, wait := l.take("10.0.0.1")
	if ok || wait <= 0 || wait > credentialEvery {
		t.Fatalf("past the burst: ok %v, wait %v", ok, wait)
	}
	if ok, _ := l.take("10.0.0.2"); !ok {
		t.Fatal("another client paid for the first one's guesses")
	}
	now = now.Add(credentialEvery)
	if ok, _ := l.take("10.0.0.1"); !ok {
		t.Fatal("no attempt came back after the refill time")
	}
	if ok, _ := l.take("10.0.0.1"); ok {
		t.Fatal("more than one attempt came back")
	}

	// Through the router: the doors share the budget and say when to retry.
	_, h := internalApp(t)
	send := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"username":"x","password":"wrong-password"}`))
		req.RemoteAddr = "192.0.2.7:5555"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for i := 0; i < credentialBurst-1; i++ {
		if w := send("/api/v1/auth/login"); w.Code == http.StatusTooManyRequests {
			t.Fatalf("login %d refused as too many", i+1)
		}
	}
	if w := send("/oauth/token"); w.Code == http.StatusTooManyRequests {
		t.Fatal("the last attempt of the burst was refused")
	}
	w := send("/api/v1/auth/bootstrap")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("an attempt past the burst on another door: %d %v", w.Code, w.Header())
	}
}

// A refused attempt never reaches the door: an OAuth code or refresh token
// sent while the client is over its budget is not spent, so after waiting
// the same credential still works.
func TestARefusedAttemptDoesNotSpendItsCredential(t *testing.T) {
	t.Parallel()
	l := newCredentialLimiter(func() time.Time { return time.Unix(1_800_000_000, 0) })
	calls := 0
	door := l.door(proxyPolicy{}, func(w http.ResponseWriter, r *http.Request) { calls++ })
	for i := 0; i < credentialBurst+3; i++ {
		req := httptest.NewRequest("POST", "/oauth/token", nil)
		req.RemoteAddr = "192.0.2.9:4000"
		door(httptest.NewRecorder(), req)
	}
	if calls != credentialBurst {
		t.Fatalf("the door ran %d times for %d admitted attempts", calls, credentialBurst)
	}
}

// privileged is the account menu's answer for user, which must be readable.
func privileged(t *testing.T, a *App, user, role, priv string) bool {
	t.Helper()
	may, err := a.hasPrivilege(context.Background(), user, role, priv)
	if err != nil {
		t.Fatal(err)
	}
	return may
}

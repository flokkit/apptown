package apptown

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/oidclogin"
	"golang.org/x/time/rate"
)

// Internal auth: most people will not run an external IdP, and requiring
// one would make an installation harder to stand up. Mode "internal":
// identity comes from a bearer token or a session cookie; the actor header
// is ignored. Attribution — the product's central claim — stops being a
// header anyone can type.
//
// Groups are the first construct of shared authority to ship: an
// approval step may name "group:approvers" and
// any member's assent satisfies it — the story cites the group, the
// ledger records the person.

const sessionCookie = httpapi.SessionCookie

// resolveSession returns the identity behind a session cookie, or nil for an
// absent/invalid cookie. Lookup failures are returned separately.
func (a *App) resolveSession(r *http.Request) (*authIdentity, error) {
	c, err := r.Cookie(a.sessionCookieName(r))
	if err != nil || c.Value == "" {
		return nil, nil
	}
	pr, err := a.Access.SessionIdentity(r.Context(), c.Value)
	if err != nil {
		if errors.Is(err, identity.ErrAccountDisabled) {
			return nil, nil
		}
		return nil, err
	}
	if pr.Name == "" {
		return nil, nil
	}
	return &authIdentity{PrincipalID: pr.ID, User: pr.Name, Role: pr.Role, Via: pr.Via, Credential: pr.Credential}, nil
}

// authOpenPaths are reachable without identity in internal mode: the login
// door itself, and the stateless local-compose rendering.
func authOpenPath(method, path string) bool {
	switch {
	case path == "/api/v1/auth/external/start", path == "/api/v1/auth/external/callback", path == "/api/v1/auth/login", path == "/api/v1/auth/me",
		path == "/api/v1/auth/bootstrap":
		return true
	// An invite link is opened before its holder has an account; the
	// invite token in the body is the credential.
	case method == "POST" && (path == "/api/v1/auth/invite" || path == "/api/v1/auth/invite/accept"):
		return true
	case method == "POST" && path == "/api/v1/complaints":
		return true
	// The contract tooling is a stateless computation every coding agent is
	// TOLD to use (apptown config schema/validate); demanding a login it cannot
	// have produced complaints 33, 34 and 36. Schema is embedded and public;
	// validate echoes the caller's own yaml against the catalog's names.
	case path == "/api/v1/config/schema",
		method == "POST" && path == "/api/v1/config/validate":
		return true
	// apptown dev / apptown dev up render the caller's own app.yaml to a local
	// docker-compose for rehearsal — a stateless computation the container-mode
	// guidance tells every coding agent to run, writing nothing (complaint 44).
	case method == "POST" && path == "/api/v1/dev-compose":
		return true
	}
	return false
}

// needsBootstrap: internal mode with no password-bearing user yet — the
// first account is created from the login screen, as admin.
func (a *App) needsBootstrap(ctx context.Context) bool {
	needs, err := a.Access.NeedsBootstrap(ctx)
	readOK(err)
	return needs
}

// handleAuthMe: GET /api/v1/auth/me — identity when present; always says
// which mode is running so the UI knows whether a login door exists.
func (a *App) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	out := httpapi.AuthMe{Mode: a.cfg.authMode}
	if a.cfg.authMode == "internal" {
		needs := a.needsBootstrap(r.Context())
		out.NeedsBootstrap = &needs
		provider, err := a.Access.ExternalProvider(r.Context())
		if err != nil {
			writeErr(w, 503, "external sign-in settings could not be read")
			return
		}
		if provider != nil && provider.Enabled {
			out.External = &httpapi.ExternalProviderSummary{DisplayName: provider.DisplayName, AllowSignup: provider.AllowSignup}
		}

	}
	if id := identityFrom(r); id != nil {
		out.User, out.Role, out.Via = id.User, id.Role, id.Via
		out.From = a.cfg.proxies.ClientIP(r).String()
		pr := principalOf(id)
		channel := access.DelegationOf(pr)
		if pr.Kind == "user" && !pr.ID.IsZero() && (channel.Kind == "full-breadth" || channel.Kind == "cli-token") {
			tokens, err := a.Access.OwnTokens(r.Context(), pr)
			if err != nil {
				writeErr(w, 503, "your access tokens could not be read")
				return
			}
			out.OwnTokens = tokens
		}
	}
	writeJSON(w, 200, out)
}

// handleAuthBootstrap: POST /api/v1/auth/bootstrap {username, password} —
// creates the FIRST account, as admin. Refused once any account exists.
func (a *App) handleAuthBootstrap(w http.ResponseWriter, r *http.Request) {
	if a.cfg.authMode != "internal" {
		writeErr(w, 404, "not in internal auth mode")
		return
	}
	var in identity.NewAccount
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "username (slug) and a password of at least 8 characters")
		return
	}
	signed, err := a.Access.Bootstrap(r.Context(), in)
	if err != nil {
		code := 500
		var invalid *identity.AccountInputError
		switch {
		case errors.Is(err, identity.ErrAlreadyBootstrapped):
			code = 409
		case errors.Is(err, identity.ErrSetupToken):
			code = 403
		case errors.As(err, &invalid):
			code = 400
		}
		writeErr(w, code, "%v", err)
		return
	}
	a.cfg.browserSession().Start(w, r, signed.Token)
	writeJSON(w, 200, map[string]any{"ok": true, "user": signed.Principal.Name, "role": signed.Principal.Role})
}

// handleAuthLogin: POST /api/v1/auth/login {username, password}.
func (a *App) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	signed, err := a.Access.SignIn(r.Context(), identity.PasswordLogin{Username: in.Username, Password: in.Password, From: a.cfg.proxies.ClientIP(r)})
	if err != nil {
		var denied *identity.AdministratorDenied
		switch {
		case errors.Is(err, identity.ErrCredentials):
			writeErr(w, 401, "%s", err)
		case errors.As(err, &denied), errors.Is(err, identity.ErrAccountDisabled):
			writeErr(w, 403, "%s", err)
		default:
			writeErr(w, 500, "%v", err)
		}
		return
	}
	a.cfg.browserSession().Start(w, r, signed.Token)
	writeJSON(w, 200, map[string]any{"ok": true, "user": signed.Principal.Name, "role": signed.Principal.Role})
}

// handleAuthLogout: POST /api/v1/auth/logout.
func (a *App) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(a.sessionCookieName(r)); err == nil {
		// Logging out IS ending the session: a cookie cleared over a row
		// that stayed would let the next holder of the cookie value in.
		if err := a.Access.SignOut(r.Context(), c.Value); err != nil {
			writeErr(w, 500, "ending the session: %v", err)
			return
		}
	}
	a.cfg.browserSession().End(w, r)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// The doors that take a secret (a password, the setup token, an invite, an
// OAuth code or refresh token) are rate-limited per client address, so a
// guess costs time: a burst of credentialBurst, then one more every
// credentialEvery. It slows guessing; it is not a lockout, and a restart
// forgets it. The address is the one AppTown already resolves through its
// trusted proxies.
const (
	credentialBurst = 10
	credentialEvery = 6 * time.Second
	credentialIdle  = 10 * time.Minute // a quiet address's budget is full again; its entry may go
	credentialSweep = 4096             // entries kept before quiet ones are swept
)

type credentialLimiter struct {
	now  func() time.Time
	mu   sync.Mutex
	byIP map[string]*credentialBudget
}

type credentialBudget struct {
	limiter *rate.Limiter
	last    time.Time
}

func newCredentialLimiter(now func() time.Time) *credentialLimiter {
	return &credentialLimiter{now: now, byIP: map[string]*credentialBudget{}}
}

// take spends one attempt from key's budget, or says how long until one is
// free.
func (l *credentialLimiter) take(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.byIP) >= credentialSweep {
		for k, b := range l.byIP {
			if now.Sub(b.last) > credentialIdle {
				delete(l.byIP, k)
			}
		}
	}
	b := l.byIP[key]
	if b == nil {
		b = &credentialBudget{limiter: rate.NewLimiter(rate.Every(credentialEvery), credentialBurst)}
		l.byIP[key] = b
	}
	b.last = now
	r := b.limiter.ReserveN(now, 1)
	if wait := r.DelayFrom(now); wait > 0 {
		r.CancelAt(now)
		return false, wait
	}
	return true, 0
}

// credentialDoor is the route table's wrapper for a door that takes a
// secret: AppTown's limiter and proxies are read when a request comes, so
// the table can be built (and inspected) before AppTown exists.
func credentialDoor(a *App, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { a.credentials.door(a.cfg.proxies, h)(w, r) }
}

// door admits a request to a door that takes a secret only while its
// client (as proxies resolve it) has attempts left; otherwise 429 with
// Retry-After.
func (l *credentialLimiter) door(proxies proxyPolicy, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.take(proxies.ClientIP(r).String())
		if !ok {
			secs := int(math.Ceil(wait.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeErr(w, http.StatusTooManyRequests, "%s", fmt.Sprintf("too many attempts from this address; try again in %d seconds", secs))
			return
		}
		h(w, r)
	}
}

func (a *App) externalCookieName(r *http.Request) string {
	if a.secureBrowser(r) {
		return "__Host-apptown_external"
	}
	return "apptown_external"
}
func (a *App) externalCookie(w http.ResponseWriter, r *http.Request, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: a.externalCookieName(r), Value: value, Path: "/", HttpOnly: true, Secure: a.secureBrowser(r), SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (a *App) handleExternalStart(w http.ResponseWriter, r *http.Request) {
	if a.cfg.authMode != "internal" {
		writeErr(w, 404, "not in internal auth mode")
		return
	}
	var in httpapi.ExternalStartRequest
	if e := readJSON(r, &in); e != nil {
		writeErr(w, 400, "invalid external sign-in request")
		return
	}
	flow, e := a.Access.BeginExternal(r.Context(), identity.ExternalStart{Name: in.Name, ReturnPath: in.ReturnPath, Password: in.Password, Link: in.Link, Principal: httpapi.Principal(r), From: a.cfg.proxies.ClientIP(r)})
	if e != nil {
		externalError(w, e)
		return
	}
	a.externalCookie(w, r, flow.BrowserSecret, 600)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, httpapi.ExternalRedirect{URL: flow.Redirect})
}
func externalError(w http.ResponseWriter, e error) {
	code := http.StatusUnauthorized
	if errors.Is(e, oidclogin.ErrUnavailable) {
		code = http.StatusServiceUnavailable
	}
	if errors.Is(e, identity.ErrExternalUnbound) {
		code = http.StatusForbidden
	}
	writeErr(w, code, "external sign-in could not complete; check the provider or start again")
}
func (a *App) handleExternalCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if a.cfg.authMode != "internal" {
		writeErr(w, 404, "not in internal auth mode")
		return
	}
	cookie, e := r.Cookie(a.externalCookieName(r))
	a.externalCookie(w, r, "", -1)
	if e != nil || r.URL.Query().Get("error") != "" {
		http.Redirect(w, r, "/?signin=failed", http.StatusSeeOther)
		return
	}
	signed, e := a.Access.CompleteExternal(r.Context(), r.URL.Query().Get("state"), cookie.Value, r.URL.Query().Get("code"), a.cfg.proxies.ClientIP(r))
	if e != nil {
		failure := "failed"
		if errors.Is(e, oidclogin.ErrUnavailable) {
			failure = "unavailable"
		}
		if errors.Is(e, identity.ErrExternalUnbound) {
			failure = "unbound"
		}
		var accountInput *identity.AccountInputError
		if errors.As(e, &accountInput) {
			failure = "name-unavailable"
		}
		http.Redirect(w, r, "/?signin="+failure, http.StatusSeeOther)
		return
	}
	a.cfg.browserSession().StartFor(w, r, signed.Token, signed.Lifetime)
	http.Redirect(w, r, signed.ReturnPath, http.StatusSeeOther)
}

func (a *App) handleExternalUnlink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	id, e := ids.Parse(r.PathValue("id"))
	if e != nil || readJSON(r, &in) != nil {
		writeErr(w, 400, "invalid unlink request")
		return
	}
	if e := a.Access.UnlinkExternal(r.Context(), id, in.Password, httpapi.Principal(r), a.cfg.proxies.ClientIP(r)); e != nil {
		externalError(w, e)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

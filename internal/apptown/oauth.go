package apptown

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/fault"
)

// The embedded OAuth authorization server for the MCP door (design/subsystems/mcp.md,
// authorization-capabilities.md). A local coding
// agent discovers this AppTown as an OAuth resource, sends the person to
// their browser, and comes back with a token that is audience-bound to
// /mcp/user, short-lived, and an upper bound on what the person may do —
// never a copy of their role.
//
// Shape: authorization-code + PKCE (S256) for public clients, dynamic client
// registration (RFC 7591) because MCP clients register themselves, opaque
// tokens with a database lookup (immediate revocation, no key management),
// rotating refresh tokens. The resource-owner ceremony is AppTown's own
// login: a session cookie in internal mode, the fronting proxy's header in
// trusted-header mode. Open mode has no identity to delegate and refuses.

// --- discovery -------------------------------------------------------------------

// handleOAuthProtectedResource: GET /.well-known/oauth-protected-resource/mcp/user
func (a *App) handleOAuthProtectedResource(w http.ResponseWriter, r *http.Request) {
	base := a.mcpBaseURL(r)
	writeJSON(w, 200, map[string]any{
		"resource":                 base + identity.AudienceUser,
		"authorization_servers":    []string{base},
		"scopes_supported":         access.ScopeNames(),
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "AppTown user MCP",
	})
}

// handleOAuthServerMetadata: GET /.well-known/oauth-authorization-server
func (a *App) handleOAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	base := a.mcpBaseURL(r)
	writeJSON(w, 200, map[string]any{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"registration_endpoint":                 base + "/oauth/register",
		"revocation_endpoint":                   base + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      access.ScopeNames(),
	})
}

// --- registration (RFC 7591) ---------------------------------------------------------

// handleOAuthRegister: POST /oauth/register — a public client names itself
// and its redirect URIs. Loopback and custom-scheme redirects are what
// desktop agents use; https is fine too. http to a non-loopback host is not.
func (a *App) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	if a.authModeName() == "open" {
		oauthError(w, 400, "invalid_request", "this AppTown runs in open mode; connecting an agent needs people to sign in: start AppTown with -auth internal or -auth trusted-header")
		return
	}
	var in struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := readJSON(r, &in); err != nil || len(in.RedirectURIs) == 0 {
		oauthError(w, 400, "invalid_client_metadata", "client_name and at least one redirect_uri are required")
		return
	}
	client, err := a.Access.RegisterOAuthClient(r.Context(), identity.Registration{Name: in.ClientName, RedirectURIs: in.RedirectURIs})
	if err != nil {
		var problem *identity.RegistrationProblem
		if errors.As(err, &problem) {
			oauthError(w, 400, "invalid_redirect_uri", "%s", problem)
		} else {
			oauthError(w, 500, "server_error", "%v", err)
		}
		return
	}
	id, name := client.ID, client.Name
	writeJSON(w, 201, map[string]any{
		"client_id":                  id,
		"client_name":                name,
		"redirect_uris":              in.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"client_id_issued_at":        a.cfg.clock.Now().Unix(),
	})
}

// --- authorize ---------------------------------------------------------------------

// browserIdentity is the person at the browser, by AppTown's own door:
// the session cookie in internal mode, the proxy's header in trusted-header
// mode. Never a bearer token (a token must not mint tokens) and never the
// open-mode actor header.
func (a *App) browserIdentity(r *http.Request) (*authIdentity, error) {
	switch a.cfg.authMode {
	case "internal":
		return a.resolveSession(r)
	case "trusted-header":
		if a.cfg.authProxySecret != "" &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-AppTown-Proxy-Secret")), []byte(a.cfg.authProxySecret)) == 1 {
			if user := r.Header.Get(a.cfg.authHeader); user != "" {
				if err := identity.ValidName(user); err != nil {
					return nil, err
				}
				role, person, err := a.ensureUser(r.Context(), user)
				if err != nil {
					return nil, err
				}
				return &authIdentity{PrincipalID: person, User: user, Role: role, Via: "proxy"}, nil
			}
		}
	}
	return nil, nil
}

func parseAuthorizeReq(r *http.Request) identity.AuthorizationRequest {
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		q = r.Form
	}
	return identity.AuthorizationRequest{ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), Scope: q.Get("scope"),
		State: q.Get("state"), Challenge: q.Get("code_challenge"), Method: q.Get("code_challenge_method"), Resource: q.Get("resource")}
}

// handleOAuthAuthorize: GET shows the consent page (or the sign-in page when
// nobody is signed in); POST records the decision and redirects back with
// a code. The page is rendered here, not by the SPA, so it works the same
// behind SSO and needs no client-side state.
func (a *App) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	if a.authModeName() == "open" {
		http.Error(w, "This AppTown runs in open mode, which has no identity to delegate. Connecting a coding agent needs internal auth or SSO.", 400)
		return
	}
	in := parseAuthorizeReq(r)
	view, err := a.Access.InspectAuthorization(r.Context(), in)
	if err != nil {
		writeAuthorizationError(w, r, in, err)
		return
	}
	client := view.Client
	id, err := a.browserIdentity(r)
	if err != nil {
		if fault.KindOf(err) == fault.Invalid {
			http.Error(w, fault.Message(err), http.StatusUnauthorized)
		} else {
			log.Printf("resolving an OAuth browser identity: %v", err)
			http.Error(w, "could not verify your identity; try again later", http.StatusServiceUnavailable)
		}
		return
	}
	if id == nil {
		a.renderOAuthPage(w, r, oauthPageData{Client: client.Name, NeedsLogin: true, Query: r.URL.RawQuery, Mode: a.authModeName()})
		return
	}
	if id.Role == "admin" {
		if ip := a.cfg.proxies.ClientIP(r); !a.adminAllowedFrom(r.Context(), ip) {
			a.audit(r.Context(), id.User, ids.Nil, "auth.admin-login-refused", id.User, map[string]any{"from": ip.String(), "via": "oauth"})
			http.Error(w, "This is an administrator account, and "+a.adminDenialText(r.Context(), ip)+".", http.StatusForbidden)
			return
		}
	}
	browser := identity.BrowserAuthorization{User: id.User, Binding: a.consentBrowser(w, r, r.Method == http.MethodGet), Request: in}
	if r.Method == http.MethodGet {
		consent, err := a.Access.PrepareConsent(r.Context(), browser)
		if err != nil {
			writeAuthorizationError(w, r, in, err)
			return
		}
		var means []string
		for _, scope := range consent.View.Scopes {
			means = append(means, access.ScopeMeans(scope))
		}
		params := authorizationParams(in)
		params["consent_token"] = consent.Token
		a.renderOAuthPage(w, r, oauthPageData{Client: consent.View.Client.Name, User: id.User, Scopes: means, Query: r.URL.RawQuery, Mode: a.authModeName(), Params: params})
		return
	}
	result, err := a.Access.DecideConsent(r.Context(), identity.ConsentDecision{Browser: browser, Token: r.PostFormValue("consent_token"), Choice: identity.ConsentChoice(r.PostFormValue("decision"))})
	if err != nil {
		writeAuthorizationError(w, r, in, err)
		return
	}
	if result.Choice == identity.ConsentDeny {
		oauthRedirectError(w, r, in.RedirectURI, in.State, "access_denied", "the person did not allow it")
		return
	}
	u, _ := url.Parse(in.RedirectURI)
	q := u.Query()
	q.Set("code", result.Code)
	if in.State != "" {
		q.Set("state", in.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// Domain rejections carry redirect safety; HTTP owns how they are presented.
func writeAuthorizationError(w http.ResponseWriter, r *http.Request, in identity.AuthorizationRequest, err error) {
	var problem *identity.AuthorizationProblem
	if errors.As(err, &problem) {
		if problem.SafeRedirect {
			oauthRedirectError(w, r, in.RedirectURI, in.State, "invalid_request", problem.Error())
		} else {
			http.Error(w, "This connection request is not valid: "+problem.Error(), 400)
		}
		return
	}
	var consent *identity.ConsentError
	if errors.As(err, &consent) {
		switch consent.Failure {
		case identity.ConsentNoBrowser, identity.ConsentChanged:
			writeErr(w, 403, "%s", consent)
		case identity.ConsentBadChoice:
			writeErr(w, 400, "%s", consent)
		case identity.ConsentIssueFailed:
			http.Error(w, consent.Error(), 500)
		default:
			writeErr(w, 500, "%s", consent)
		}
		return
	}
	writeErr(w, 500, "%v", err)
}

func oauthRedirectError(w http.ResponseWriter, r *http.Request, redirect, state, code, desc string) {
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, desc, 400)
		return
	}
	q := u.Query()
	q.Set("error", code)
	q.Set("error_description", desc)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

type oauthPageData struct {
	Client     string
	User       string
	Scopes     []string
	NeedsLogin bool
	Query      string
	Mode       string
	// Params are the authorize request's fields, carried in the consent
	// form as hidden inputs so the decision binds to exactly what the page
	// showed (a query string in the form action gets escaped by the
	// template engine, which is how the first live test failed).
	Params map[string]string
}

func authorizationParams(in identity.AuthorizationRequest) map[string]string {
	m := map[string]string{"response_type": "code", "client_id": in.ClientID, "redirect_uri": in.RedirectURI,
		"code_challenge": in.Challenge, "code_challenge_method": in.Method}
	for k, v := range map[string]string{"scope": in.Scope, "state": in.State, "resource": in.Resource} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}

var oauthPage = template.Must(template.New("oauth").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connect {{.Client}} to AppTown</title>
<style>
body{margin:0;font:15px/1.5 system-ui,sans-serif;background:#0f1117;color:#e6e8ef;display:grid;place-items:center;min-height:100vh}
.card{background:#181b25;border:1px solid #2a2e3d;border-radius:12px;padding:28px;max-width:440px;width:calc(100% - 40px)}
h1{font-size:18px;margin:0 0 6px}.muted{color:#9aa1b5}ul{padding-left:18px;margin:8px 0}
input{width:100%;box-sizing:border-box;padding:9px 10px;border-radius:8px;border:1px solid #2a2e3d;background:#0f1117;color:inherit;margin:6px 0}
button{padding:9px 16px;border-radius:8px;border:1px solid #2a2e3d;background:#242938;color:inherit;cursor:pointer;font:inherit}
button.primary{background:#4f6ef7;border-color:#4f6ef7;color:#fff}.row{display:flex;gap:10px;margin-top:16px;align-items:center}
.err{color:#ff8a8a;margin-top:8px;min-height:1.2em}
</style></head><body><div class="card">
{{if .NeedsLogin}}
<h1>Sign in to connect {{.Client}}</h1>
<p class="muted">{{.Client}} wants to use your AppTown account. Sign in first; you will then see exactly what it may do.</p>
{{if eq .Mode "internal"}}
<form id="login"><input name="username" placeholder="username" autocomplete="username" required>
<input name="password" type="password" placeholder="password" autocomplete="current-password" required>
<div class="row"><button class="primary" type="submit">Sign in</button></div><div class="err" id="err"></div></form>
<script>
document.getElementById('login').onsubmit=async e=>{e.preventDefault();const f=new FormData(e.target);
const r=await fetch('/api/v1/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username:f.get('username'),password:f.get('password')})});
if(r.ok){location.reload()}else{document.getElementById('err').textContent=(await r.json().catch(()=>({}))).error||'sign-in failed'}};
</script>
{{else}}<p class="muted">Your sign-on did not reach this page. Open AppTown in this browser, sign in, and try again.</p>{{end}}
{{else}}
<h1>{{.Client}} wants to use your AppTown account</h1>
<p class="muted">Signed in as <b>{{.User}}</b>. If allowed, {{.Client}} can:</p>
<ul>{{range .Scopes}}<li>{{.}}</li>{{end}}</ul>
<p class="muted">It cannot approve, land, administer, or write main. You can disconnect it any time from People → Connected coding agents.</p>
<form method="post" action="/oauth/authorize">
{{range $k, $v := .Params}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<div class="row"><button class="primary" name="decision" value="allow">Allow</button><button name="decision" value="deny">Cancel</button></div>
</form>
{{end}}
</div></body></html>`))

func (a *App) renderOAuthPage(w http.ResponseWriter, _ *http.Request, d oauthPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = oauthPage.Execute(w, d)
}

// --- token ---------------------------------------------------------------------------

func oauthError(w http.ResponseWriter, status int, code, format string, args ...any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]any{"error": code, "error_description": fmt.Sprintf(format, args...)})
}

// handleOAuthToken: POST /oauth/token (form-encoded). authorization_code
// with PKCE, or refresh_token (rotating).
func (a *App) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "form-encoded body required")
		return
	}
	switch r.FormValue("grant_type") {
	case "authorization_code":
		a.oauthExchangeCode(w, r)
	case "refresh_token":
		a.oauthRefresh(w, r)
	default:
		oauthError(w, 400, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

func (a *App) oauthExchangeCode(w http.ResponseWriter, r *http.Request) {
	pair, err := a.Access.ExchangeCode(r.Context(), identity.CodeExchange{Code: r.FormValue("code"), Verifier: r.FormValue("code_verifier"), ClientID: r.FormValue("client_id"), RedirectURI: r.FormValue("redirect_uri"), AuthMethod: a.authModeName()})
	if err != nil {
		var problem *identity.CodeExchangeProblem
		if errors.As(err, &problem) {
			oauthError(w, 400, "invalid_grant", "%s", problem)
		} else {
			oauthError(w, 500, "server_error", "%v", err)
		}
		return
	}
	writeTokenPair(w, pair, pair.Scopes)
}

func (a *App) oauthRefresh(w http.ResponseWriter, r *http.Request) {
	pair, err := a.Access.RefreshForClient(r.Context(), r.FormValue("refresh_token"), r.FormValue("client_id"))
	if err != nil {
		if errors.Is(err, identity.ErrLeaked) || errors.Is(err, identity.ErrUnknownRefresh) || errors.Is(err, identity.ErrConnectionEnded) || errors.Is(err, identity.ErrClientMismatch) {
			oauthError(w, 400, "invalid_grant", "%v", err)
		} else {
			oauthError(w, 500, "server_error", "%v", err)
		}
		return
	}
	writeTokenPair(w, pair, pair.Scopes)
}

func writeTokenPair(w http.ResponseWriter, p identity.Pair, scopes []string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{
		"access_token": p.Access, "token_type": "Bearer", "expires_in": int(identity.AccessTTL.Seconds()),
		"refresh_token": p.Refresh, "scope": strings.Join(scopes, " "),
	})
}

// handleOAuthRevoke: POST /oauth/revoke (token=…) — RFC 7009. Revoking
// either token of a pair ends the delegation: a client that wants out is
// out.
func (a *App) handleOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if err := a.Access.RevokeOAuthToken(r.Context(), r.FormValue("token")); err != nil {
		oauthError(w, 500, "server_error", "the revoke could not be recorded")
		return
	}
	w.WriteHeader(200) // RFC 7009: unknown tokens are 200 too
}

// --- resolving a token -----------------------------------------------------------------

func (a *App) delegationByID(ctx context.Context, id ids.ID) *identity.Delegation {
	d, err := a.Access.LiveConnection(ctx, id)
	readOK(err)
	return d
}

// resolveOAuthToken returns an access token identity for the audience, nil for
// an invalid credential, and an error when lookup is unavailable. The
// person's CURRENT role and standing answer; the token contributes only the
// delegation's bounds (§10).
func (a *App) resolveOAuthToken(ctx context.Context, tok, audience string) (*authIdentity, error) {
	resolved, err := a.Access.ResolveOAuth(ctx, tok, audience)
	if err != nil {
		if errors.Is(err, identity.ErrOAuthCredential) {
			return nil, nil
		}
		return nil, err
	}
	pr := resolved.Principal
	return &authIdentity{PrincipalID: pr.ID, User: pr.Name, Role: pr.Role, Via: pr.Via, DelegationID: pr.DelegationID, ClientID: resolved.ClientID, Scopes: pr.Scopes, Credential: pr.Credential}, nil
}

// --- the person's connected agents ---------------------------------------------------------

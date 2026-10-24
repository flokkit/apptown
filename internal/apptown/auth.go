package apptown

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/fault"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

// Multi-user (design/subsystems/auth.md). Identity comes from a bearer token
// (CLI/API/automation), a session cookie (internal mode — AppTown's own
// passwords, internal_auth.go), a trusted reverse-proxy header (Authentik
// forward-auth), or — in open mode — the legacy actor header. Roles are
// two: admin (instance surface) and member (project surface).
//
// AppTown does its own passwords: requiring an external IdP would make an
// installation harder to stand up, and a header anyone can type is not
// attribution. External IdPs stay first-class via trusted-header mode.

type authIdentity struct {
	// PrincipalID is the person who answers (access.Principal.ID); Nil for a
	// run, an anonymous caller, or a name whose account could not be read.
	PrincipalID ids.ID
	User        string
	Role        string // "admin" | "member"
	Via         string // "token" | "session" | "proxy" | "open" | "console" | "run" | "oauth"
	// Purpose is a personal token's: access.TokenCLI or access.TokenAgent.
	Purpose string
	// ProjectID scopes an instrument identity (a console session, a step
	// agent) to the one project it works in; ConsoleID names the session.
	// The route guards (routes.go) let such an identity act only there.
	ProjectID ids.ID
	ConsoleID string
	// An OAuth delegation (oauth.go): the client the person authorized and
	// the scopes they consented to. The kernel narrows by them; nothing here
	// widens what the person may do.
	DelegationID ids.ID
	ClientID     string
	Scopes       []string
	// Credential is the non-secret reference to the token, session or
	// delegation behind the request (identity.TokenCredential and kin).
	Credential string
}

type authCtxKey struct{}

func identityFrom(r *http.Request) *authIdentity {
	if v, ok := r.Context().Value(authCtxKey{}).(*authIdentity); ok {
		return v
	}
	return nil
}

// SeedAdministrators explicitly initializes root grants at normal startup.
// Configuration and verify-only construction never write grants.
func (a *App) SeedAdministrators(ctx context.Context, adminSeed []string) {
	for _, u := range adminSeed {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if err := a.Access.SeedAdministrator(ctx, u); err != nil {
			log.Printf("seeding administrator %s: %v", u, err)
		}
	}
}

func (a *App) ensureUser(ctx context.Context, username string) (string, ids.ID) {
	account, err := a.Access.EnsurePerson(ctx, username)
	if err != nil {
		log.Printf("recording user %s: %v", username, err)
		return "member", ids.Nil
	}
	return account.Role, account.PrincipalID
}

// AuthMiddleware resolves identity for every /api request. Resolution order:
// bearer token → session cookie (internal mode) → trusted proxy header
// (trusted-header mode) → actor header (open mode only) → 401.
func (a *App) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r) // static SPA; the fronting proxy guards it
			return
		}
		// The API description is the build's shipped document, the same for
		// every caller and naming no record: a client reads it before it
		// has a credential, in every auth mode.
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/openapi.json" {
			next.ServeHTTP(w, r)
			return
		}
		var id *authIdentity
		if hasBearer(r) {
			id = a.resolveBearer(r)
			if id == nil {
				writeErr(w, 401, "invalid token")
				return
			}
		}
		if id == nil && a.cfg.authMode == "internal" {
			id = a.resolveSession(r)
			if id == nil {
				// Console containers hold a per-session token, not a cookie.
				// Attribution keeps the console provenance string, and no
				// users row is minted — a console is an instrument the person
				// wields, not a person (the open-mode sediment of one row per
				// chat session was a bug in institutional terms).
				if cs, ok := a.Console.Identity(r.Header.Get("X-AppTown-Session-Token")); ok {
					opener, _ := ids.Parse(cs.Labels[conversation.OpenerLabel])
					id = &authIdentity{PrincipalID: opener, User: cs.Actor + " via console/" + cs.SessionID, Role: "member", Via: "console",
						ProjectID: conversation.FromLabels(cs.Labels).ProjectID, ConsoleID: cs.SessionID}
					// A chat acts for its person: once their account is
					// disabled it makes no new request, here as at inference,
					// including acts no capability decision covers (a
					// complaint). The chat itself stays with its change.
					if err := a.Access.RequireEnabled(r.Context(), principalOf(id, "")); err != nil {
						var denied *access.Denied
						if errors.As(err, &denied) {
							writeErr(w, 403, "%s", denied.Reason)
						} else {
							writeErr(w, 503, "reading this chat's account: %v", err)
						}
						return
					}
				} else if who, err := inferenceIdentityFor(a).AuthenticateStep(r.Context(), r.Header.Get("X-AppTown-Session-Token")); err == nil {
					// A step agent: same species as a console session. The
					// token is checked as the inference gateway checks it
					// (the attempt is live, its owner enabled). The
					// read-only port bounds what "member" can do here.
					id = &authIdentity{User: fmt.Sprintf("run/%d", who.RunID), Role: "member", Via: "run", ProjectID: who.ProjectID}
				}
			}
			if id == nil {
				if authOpenPath(r.Method, r.URL.Path) {
					next.ServeHTTP(w, r)
					return
				}
				writeErr(w, 401, "sign in required")
				return
			}
		}
		if id == nil && a.cfg.authMode == "trusted-header" {
			if a.cfg.authProxySecret != "" &&
				subtle.ConstantTimeCompare([]byte(r.Header.Get("X-AppTown-Proxy-Secret")), []byte(a.cfg.authProxySecret)) == 1 {
				if user := r.Header.Get(a.cfg.authHeader); user != "" {
					// A name AppTown would read as something else
					// ("x via console/y", "run/3") is refused, not recorded.
					if err := identity.ValidName(user); err != nil {
						writeErr(w, 401, "the identity provider's user name is not usable here: %s", fault.Message(err))
						return
					}
					role, person := a.ensureUser(r.Context(), user)
					id = &authIdentity{PrincipalID: person, User: user, Role: role, Via: "proxy"}
				}
			}
			if id == nil {
				writeErr(w, 401, "authentication required (trusted-header mode)")
				return
			}
		}
		if id == nil { // open mode
			user := r.Header.Get("X-AppTown-Actor")
			if user == "" {
				user = "anonymous"
			}
			if err := identity.ValidName(user); err != nil {
				writeErr(w, 401, "X-AppTown-Actor is not usable as a name: %s", fault.Message(err))
				return
			}
			role, person := a.ensureUser(r.Context(), user)
			id = &authIdentity{PrincipalID: person, User: user, Role: role, Via: "open"}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authCtxKey{}, id)))
	})
}

// hasBearer reports whether the request carries an Authorization: Bearer
// header at all (a bad one is a 401, not a fall-through to another door).
func hasBearer(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// resolveBearer resolves a personal access token to an identity, or nil for
// an unknown/revoked token. Shared by the /api middleware and the MCP
// endpoint (mcp_server.go), which live outside the middleware's path prefix.
func (a *App) resolveBearer(r *http.Request) *authIdentity {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	pr, err := a.Access.PersonalTokenIdentity(r.Context(), tok, a.cfg.proxies.ClientIP(r))
	if err != nil {
		var denied *identity.AdministratorDenied
		if !errors.As(err, &denied) && !errors.Is(err, identity.ErrAccountDisabled) {
			log.Printf("resolving a token: %v", err)
		}
		return nil
	}
	if pr.Name == "" {
		return nil
	}
	return &authIdentity{PrincipalID: pr.ID, User: pr.Name, Role: pr.Role, Via: pr.Via, Purpose: pr.Purpose, Credential: pr.Credential}
}

// handleMe: GET /api/v1/me
func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id == nil {
		writeErr(w, 401, "unauthenticated")
		return
	}
	writeJSON(w, 200, map[string]any{"user": id.User, "role": id.Role, "via": id.Via, "mode": a.authModeName()})
}

func (a *App) authModeName() string {
	if a.cfg.authMode == "" {
		return "open"
	}
	return a.cfg.authMode
}

func (a *App) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	return httpapi.RequireAdmin(a.Access, w, r.WithContext(callerContext(r)))
}

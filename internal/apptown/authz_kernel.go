package apptown

import (
	"context"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/httpapi"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
)

// The authorization kernel's rule lives in internal/access (Decide, §12).
// AppTown keeps what needs the app: the principal from the request, the
// resources with their containing scopes, the facts the rule reads, the
// receipt, and the HTTP glue.

// principalOf converts an admitted identity into an authorization principal.
// Receipt attribution never supplies a missing identity.
func principalOf(id *authIdentity) access.Principal {
	if id == nil {
		return access.Principal{Kind: "anonymous", Name: "anonymous", Via: "open"}
	}

	switch id.Via {
	case "console":
		person, _, _ := strings.Cut(id.User, " via console/")
		return access.Principal{ID: id.PrincipalID, Kind: "instrument", Name: id.User, Person: person, Instrument: "console",
			Via: id.Via, ProjectID: id.ProjectID, ConsoleID: id.ConsoleID, Role: id.Role}
	case "run":
		return access.Principal{Kind: "instrument", Name: id.User, Instrument: "run", Via: id.Via,
			ProjectID: id.ProjectID, Role: id.Role}
	}
	if id.User == "" || id.User == "anonymous" {
		return access.Principal{Kind: "anonymous", Name: "anonymous", Via: id.Via}
	}
	credential := id.Credential
	if credential == "" && (id.Via == "proxy" || id.Via == "open") {
		// A trusted proxy or open mode attributed the request; there is no
		// credential AppTown could revoke, only the account.
		credential = identity.HeaderCredential(id.User)
	}
	return access.Principal{ID: id.PrincipalID, Kind: "user", Name: id.User, Via: id.Via, Role: id.Role, Purpose: id.Purpose, DelegationID: id.DelegationID, Scopes: id.Scopes, Credential: credential}
}

// --- resources ----------------------------------------------------------------

func (a *App) resourceInstance() access.Resource { return access.Resource{Type: "instance"} }

// --- HTTP glue ------------------------------------------------------------------

// authorizeRequest is what a route guard or a handler calls: principal from
// the request, a receipt, and a 403 written in plain words on denial.
func (a *App) authorizeRequest(w http.ResponseWriter, r *http.Request, c access.Capability, res access.Resource, ctx access.Context) (access.Decision, bool) {
	return httpapi.AuthorizeRequest(a.Access, w, r.WithContext(callerContext(r)), c, res, ctx)
}

// holds answers a yes/no for the account menu, with no receipt: it is a
// question, not an act. A fact that cannot be read is an error.
func (a *App) holds(ctx context.Context, pr access.Principal, c access.Capability, res access.Resource) (bool, error) {
	dec, err := a.Access.Explain(ctx, pr, access.Permission{Capability: c, Resource: res})
	return dec.Allow, err
}

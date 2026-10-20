package forge

import (
	"context"
	"net/http"
	"strings"

	"flokkit.io/forge/internal/forge/httpapi"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
)

// The authorization kernel's rule lives in internal/access (Decide, §12).
// Forge keeps what needs the app: the principal from the request, the
// resources with their containing scopes, the facts the rule reads, the
// receipt, and the HTTP glue.

// principalOf turns the auth layer's identity into a kernel principal. In
// open mode a bare actor header is a user principal; that is the design's
// stated incompatibility (mcp.md §3), tolerated on the legacy HTTP surface
// only until the bootstrap owner exists.
func principalOf(id *authIdentity, fallbackActor string) access.Principal {
	if id == nil {
		if fallbackActor == "" || fallbackActor == "anonymous" {
			return access.Principal{Kind: "anonymous", Name: "anonymous", Via: "open"}
		}
		return access.Principal{Kind: "user", Name: fallbackActor, Via: "open", Credential: identity.HeaderCredential(fallbackActor)}
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

func (a *App) resourceProject(ctx context.Context, p *project.Project) access.Resource {
	return project.ObservedResource(ctx, a.Projects, p)
}

func (a *App) resourceReleaseTarget(ctx context.Context, target string, p *project.Project) access.Resource {
	r := a.resourceProject(ctx, p)
	r.Type, r.ID = "release-target", target
	return r
}

// --- HTTP glue ------------------------------------------------------------------

// authorizeRequest is what a route guard or a handler calls: principal from
// the request, a receipt, and a 403 written in plain words on denial.
func (a *App) authorizeRequest(w http.ResponseWriter, r *http.Request, c access.Capability, res access.Resource, ctx access.Context) (access.Decision, bool) {
	return httpapi.AuthorizeRequest(a.Access, w, r.WithContext(callerContext(r)), c, res, ctx)
}

// holds answers a plain yes/no for UI and the legacy privilege helpers, with
// no receipt: it is a question, not an act.
func (a *App) holds(ctx context.Context, pr access.Principal, c access.Capability, res access.Resource) bool {
	return a.Access.Evaluate(ctx, pr, access.DelegationOf(pr), c, res, access.Context{}).Allow
}

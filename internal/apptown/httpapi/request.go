package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/fault"
)

// ProjectFromPath reads the project the path names. Only an absence the
// project owner classified is "not found"; a failed read stays a failure, so
// an outage is never reported as a missing project.
func ProjectFromPath(projects *project.Projects, w http.ResponseWriter, r *http.Request) (*project.Project, bool) {
	p, err := projects.BySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeReadFault(w, err, "no such project")
		return nil, false
	}
	return &p, true
}

// writeReadFault answers an owner's read: a 404 in the request's own words
// ("no such <thing>") when the owner classified the error as an absence,
// the owner's classification otherwise, so a failed read is never reported
// as a missing record.
func writeReadFault(w http.ResponseWriter, err error, absent string, args ...any) {
	if fault.KindOf(err) == fault.NotFound {
		WriteError(w, http.StatusNotFound, absent, args...)
		return
	}
	WriteFault(w, err)
}

// AuthorizeRequest uses the principal established by authentication and route
// assembly. It does not interpret headers or change the authority's policy.
func AuthorizeRequest(authority *identity.Service, w http.ResponseWriter, r *http.Request, c access.Capability, res access.Resource, ctx access.Context) (access.Decision, bool) {
	caller, ok := access.CallerFrom(r.Context())
	if !ok {
		caller.Principal = access.Principal{Kind: "anonymous", Name: "anonymous", Via: "open"}
	}
	ctx.Consequential = true
	if ctx.Route == "" {
		ctx.Route = r.Method + " " + r.URL.Path
	}
	dec, err := authority.Decide(r.Context(), caller.Principal, access.Permission{Capability: c, Resource: res, Context: ctx})
	if err != nil {
		// Unreadable authority is neither a refusal nor a permission.
		WriteError(w, http.StatusServiceUnavailable, "checking authority: %v", err)
		return access.Decision{}, false
	}
	if !dec.Allow {
		w.Header().Set("X-AppTown-Deny-Code", dec.Code)
		WriteError(w, 403, "%s", dec.Reason)
		return dec, false
	}
	return dec, true
}

// Actor is receipt attribution supplied by the transport, not authentication.
func Actor(r *http.Request) string {
	caller, ok := access.CallerFrom(r.Context())
	if !ok {
		return "anonymous"
	}
	return caller.Receipt
}

func Principal(r *http.Request) access.Principal {
	caller, ok := access.CallerFrom(r.Context())
	if !ok {
		return access.Principal{Kind: "anonymous", Name: "anonymous", Via: "open"}
	}
	return caller.Principal
}

func RequireProjectMember(authority *identity.Service, w http.ResponseWriter, r *http.Request, projectID ids.ID, slug string) bool {
	caller, ok := access.CallerFrom(r.Context())
	if !ok || !caller.Verified {
		WriteError(w, http.StatusForbidden, "an authenticated caller is required; sign in on the web, or send a command-line or agent token")
		return false
	}
	user, role := caller.Principal.Name, caller.Principal.Role
	// Console-provenance identities inherit the person's membership: the
	// roster names people, and "alice via console/x" is alice at a keyboard.
	if base, _, ok := strings.Cut(user, " via console/"); ok {
		user = base
	}
	if user != "" && user != "anonymous" && authority.HasProjectRole(r.Context(), access.Principal{Kind: "user", Name: user, Role: role}, projectID) {
		return true
	}
	WriteError(w, 403, "%s is members-only — %s is not on its roster (project settings → Members; an admin can add people)",
		slug, user)
	return false
}

// requireAdmin gates the instance surface. Returns false after writing 403.
// IsAdmin reports whether the verified caller administers this AppTown,
// for a read that shows administrators more; any failure to say is no.
func IsAdmin(authority *identity.Service, r *http.Request) bool {
	caller, ok := access.CallerFrom(r.Context())
	if !ok || !caller.Verified {
		return false
	}
	_, err := authority.Require(r.Context(), caller.Principal, access.Permission{Capability: access.CapIdentityAuth, Resource: access.Resource{Type: "instance"}})
	return err == nil
}

func RequireAdmin(authority *identity.Service, w http.ResponseWriter, r *http.Request) bool {
	if caller, ok := access.CallerFrom(r.Context()); ok && caller.Verified {
		_, err := authority.Require(r.Context(), caller.Principal, access.Permission{Capability: access.CapIdentityAuth, Resource: access.Resource{Type: "instance"}})
		if err == nil {
			return true
		}
		var denied *access.Denied
		if !errors.As(err, &denied) && !errors.Is(err, access.ErrPrincipalRequired) {
			WriteFault(w, err)
			return false
		}
	}
	WriteError(w, 403, "admin role required; ask an administrator to do this")
	return false
}

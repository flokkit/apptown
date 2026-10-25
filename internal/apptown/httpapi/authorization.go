package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/fault"
)

type Authorization struct {
	access   *identity.Service
	projects *project.Projects
}

func NewAuthorization(access *identity.Service, projects *project.Projects) Authorization {
	return Authorization{access: access, projects: projects}
}

// groupsWhereCanCreate lists the groups a person may create projects in,
// for the picker that appears only when the default will not do.
func (h Authorization) groupsWhereCanCreate(ctx context.Context, pr access.Principal) ([]string, error) {
	names, err := h.access.GroupNames(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, g := range names {
		may, err := h.holds(ctx, pr, access.CapProjectCreate, access.Resource{Type: "group", ID: g})
		if err != nil {
			return nil, err
		}
		if may {
			out = append(out, g)
		}
	}
	return out, nil
}

// holds answers whether pr may, as a question: no receipt, and a
// fact that cannot be read is an error.
func (h Authorization) holds(ctx context.Context, pr access.Principal, c access.Capability, res access.Resource) (bool, error) {
	dec, err := h.access.Explain(ctx, pr, access.Permission{Capability: c, Resource: res})
	return dec.Allow, err
}

// AuthzRoles: GET /api/v1/authz/roles — the catalog. Surfaced roles are
// what pickers offer; the rest exist for later profiles.
func (h Authorization) AuthzRoles(w http.ResponseWriter, r *http.Request) {
	out := []RoleView{}
	for _, rv := range access.StockRoles {
		caps := make([]string, 0, len(rv.Caps))
		for _, c := range rv.Caps {
			caps = append(caps, string(c))
		}
		out = append(out, RoleView{Name: rv.Name, Title: rv.Title, Description: rv.Description,
			Scope: string(rv.Scope), Surfaced: rv.Surfaced, Caps: caps})
	}
	caps := map[string]CapabilityView{}
	for c, def := range access.Definitions {
		caps[string(c)] = CapabilityView{Description: def.Description, Risk: def.Risk}
	}
	WriteJSON(w, 200, RoleCatalog{Roles: out, Capabilities: caps})
}

// AuthzGrants: GET /api/v1/authz/grants?scope=instance|project:<id>|group:<name>|all
func (h Authorization) AuthzGrants(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	var gs []access.Grant
	var err error
	switch {
	case scope == "" || scope == "all":
		gs, err = h.access.AllGrants(r.Context())
	case scope == "instance":
		gs, err = h.access.GrantsAt(r.Context(), access.ScopeInstance, "")
	default:
		kind, id, _ := strings.Cut(scope, ":")
		gs, err = h.access.GrantsAt(r.Context(), access.ScopeKind(kind), id)
	}
	if err != nil {
		WriteFault(w, err)
		return
	}
	out := []GrantView{}
	for _, g := range gs {
		out = append(out, grantOutput(r.Context(), h.projects, g))
	}
	WriteJSON(w, 200, GrantList{Grants: out})
}

// AuthzGrant: POST /api/v1/authz/grants {subject, role|capability, scope, scopeId, reason?, expiresAt?}
func (h Authorization) AuthzGrant(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "subject, role or capability, and scope are required"}, func(in struct {
		Subject, Role, Capability, Scope, ScopeID, Reason, ExpiresAt string
	}) {
		if in.Subject == "" || (in.Role == "" && in.Capability == "") || in.Scope == "" {
			WriteError(w, 400, "subject, role or capability, and scope are required")
			return
		}

		if in.Role != "" && access.RoleByName(in.Role) == nil {
			WriteError(w, 400, "no such role %q", in.Role)
			return
		}
		if in.Capability != "" {
			if _, ok := access.Definitions[access.Capability(in.Capability)]; !ok {
				WriteError(w, 400, "no such capability %q", in.Capability)
				return
			}
		}
		scope := access.ScopeKind(in.Scope)
		if rv := access.RoleByName(in.Role); rv != nil && rv.Scope != scope {
			WriteError(w, 400, "%s is a %s-scoped role; grant it at a %s", rv.Title, rv.Scope, rv.Scope)
			return
		}
		res := access.Resource{Type: "instance"}
		if scope == access.ScopeProject {
			pid, err := ids.Parse(in.ScopeID)
			if err != nil {
				WriteError(w, 404, "no such project")
				return
			}
			res, err = h.projects.AuthorityResource(r.Context(), pid)
			if err != nil {
				WriteFault(w, err)
				return
			}
		}
		// Who may grant: authorization:manage-grants at the instance, or
		// project:manage-members on the project itself.
		c := access.CapAuthzGrants
		if scope == access.ScopeProject {
			c = access.CapProjectManageMembers
		}
		if _, ok := AuthorizeRequest(h.access, w, r, c, res, access.Context{}); !ok {
			return
		}
		st, subj := identity.ParseSubject(in.Subject)
		if st == "user" {
			optionalUser(r.Context(), h.access, subj)
		}
		id, err := h.access.Grant(r.Context(), identity.Spec{SubjectType: st, Subject: subj, Role: in.Role, Capability: in.Capability,
			Scope: scope, ScopeID: in.ScopeID, By: Actor(r), Reason: in.Reason}, in.ExpiresAt)
		if err != nil {
			if !WriteRefusal(w, err) {
				WriteError(w, 500, "the grant was not written: %v", err)
			}
			return
		}
		WriteJSON(w, 201, GrantCreated{ID: id})
	})
}

// AuthzRevoke: DELETE /api/v1/authz/grants/{id}
func (h Authorization) AuthzRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	scopeType, scopeID, found, err := h.access.LiveGrant(r.Context(), id)
	if err != nil {
		WriteError(w, 500, "reading the grant: %v", err)
		return
	}
	if !found {
		WriteError(w, 404, "no such live grant")
		return
	}
	res, c := access.Resource{Type: "instance"}, access.CapAuthzGrants
	if scopeType == access.ScopeProject {
		pid, err := ids.Parse(scopeID)
		if err != nil {
			WriteError(w, 500, "the grant has an invalid project scope")
			return
		}
		projectResource, err := h.projects.AuthorityResource(r.Context(), pid)
		if err != nil && fault.KindOf(err) != fault.NotFound {
			WriteFault(w, err)
			return
		}
		if err == nil {
			res, c = projectResource, access.CapProjectManageMembers
		}
		// Only a confirmed missing project retains instance-authorized grant cleanup.
	}
	if _, ok := AuthorizeRequest(h.access, w, r, c, res, access.Context{}); !ok {
		return
	}
	_, err = h.access.Revoke(r.Context(), id, Actor(r))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

// AuthzExplain: GET /api/v1/authz/explain?capability=&project=<slug>&user=
// — "why can Ana do this?" (and why not). A question, so no receipt.
// Asking about yourself on a project you can read needs only that read:
// who may approve or land there is part of the project's own record.
// Anything else (another person, or the instance) reads the institution's
// structure and needs institution:read-structure at the instance.
func (h Authorization) AuthzExplain(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c := access.Capability(q.Get("capability"))
	if _, ok := access.Definitions[c]; !ok {
		WriteError(w, 400, "capability required")
		return
	}
	user := q.Get("user")
	self := user == "" || user == Actor(r)
	if user == "" {
		user = Actor(r)
	}
	res := access.Resource{Type: "instance"}
	if slug := q.Get("project"); slug != "" {
		p, err := h.projects.BySlug(r.Context(), slug)
		if err != nil {
			writeReadFault(w, err, "no such project")
			return
		}
		res, err = h.projects.AuthorityResource(r.Context(), p.ID)
		if err != nil {
			WriteFault(w, err)
			return
		}
	}
	if !res.ProjectID.IsZero() && self {
		if _, ok := AuthorizeRequest(h.access, w, r, access.CapProjectRead, res, access.Context{}); !ok {
			return
		}
	} else if _, ok := AuthorizeRequest(h.access, w, r, access.CapInstitutionRead, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	role, _, err := h.access.Role(r.Context(), user)
	if err != nil {
		WriteFault(w, err)
		return
	}
	pr := access.Principal{Kind: "user", Name: user, Role: role}
	if user == "anonymous" {
		pr = access.Principal{Kind: "anonymous", Name: "anonymous"}
	}
	dec, err := h.access.Explain(r.Context(), pr, access.Permission{Capability: c, Resource: res})
	if err != nil {
		WriteFaultOr(w, err, http.StatusServiceUnavailable)
		return
	}
	// Who can: every subject with a live grant that applies here.
	who := []string{}
	seen := map[string]bool{}
	grants, err := h.access.AllGrants(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	for _, g := range grants {
		if !g.Applies(c, res) {
			continue
		}
		label := g.Subject
		if g.SubjectType == "group" {
			label = "group:" + g.Subject
		}
		if !seen[label] {
			seen[label] = true
			who = append(who, label)
		}
	}
	WriteJSON(w, 200, AuthzExplanation{User: user, Capability: c, Decision: dec, WhoCan: who})
}

// AuthzMe: what the signed-in person holds where, for the UI to show
// and hide controls. Never a security boundary — the kernel is.
func (h Authorization) AuthzMe(w http.ResponseWriter, r *http.Request) {
	pr := Principal(r)
	out := AuthzHoldings{User: pr.Name}
	inst := map[string]bool{}
	// The instance reads say which instance-wide pages the person can open,
	// so the menu can leave the others out.
	for _, c := range []access.Capability{access.CapAuthzGrants, access.CapIdentityUsers, access.CapIdentityGroups, access.CapIdentityAuth, access.CapProjectCreate,
		access.CapStandardsGuidance, access.CapExecWorkflows, access.CapReleasePromote, access.CapAuditRead, access.CapSecretReveal,
		access.CapInstitutionRead, access.CapProjectRead, access.CapDeploymentRead, access.CapConformanceRead, access.CapStandardsPacks, access.CapPlatformTargets} {
		may, err := h.holds(r.Context(), pr, c, access.Resource{Type: "instance"})
		if err != nil {
			WriteFaultOr(w, err, http.StatusServiceUnavailable)
			return
		}
		inst[string(c)] = may
	}
	out.Instance = inst
	// Where this person may create projects, and where one goes by default —
	// the wizard shows a picker only when the default will not do (§3A).
	if pr.Kind == "user" {
		createIn, err := h.groupsWhereCanCreate(r.Context(), pr)
		if err != nil {
			WriteFault(w, err)
			return
		}
		if createIn == nil {
			createIn = []string{}
		}
		out.CreateIn = &createIn
		g, err := h.access.OwningGroupFor(r.Context(), pr, "")
		if err != nil {
			WriteFaultOr(w, err, http.StatusServiceUnavailable)
			return
		}
		out.DefaultGroup = &g
	}
	if slug := r.URL.Query().Get("project"); slug != "" {
		p, err := h.projects.BySlug(r.Context(), slug)
		if err != nil && fault.KindOf(err) != fault.NotFound {
			WriteFaultOr(w, err, http.StatusServiceUnavailable)
			return
		}
		if err == nil {
			res, err := h.projects.AuthorityResource(r.Context(), p.ID)
			if err != nil {
				WriteFault(w, err)
				return
			}
			pc := map[string]bool{}
			for _, c := range []access.Capability{access.CapChangeCreate, access.CapChangeRequestLand, access.CapChangeLand, access.CapChangeApprove, access.CapChangeOverride,
				access.CapChangeAbandon, access.CapProjectManageMembers, access.CapProjectConfigure, access.CapProjectWorkflow, access.CapProjectGuidance,
				access.CapProjectIntegrations, access.CapProjectServices, access.CapReleasePromote} {
				may, err := h.holds(r.Context(), pr, c, res)
				if err != nil {
					WriteFaultOr(w, err, http.StatusServiceUnavailable)
					return
				}
				pc[string(c)] = may
			}
			out.Project = pc
		}
	}
	WriteJSON(w, 200, out)
}

// GrantCreated is the id of a grant just written.
type GrantCreated struct {
	ID ids.ID `json:"id"`
}

// AuthzExplanation says whether a person may do something here, why, and who
// else may.
type AuthzExplanation struct {
	User       string            `json:"user"`
	Capability access.Capability `json:"capability"`
	Decision   access.Decision   `json:"decision"`
	WhoCan     []string          `json:"whoCan"`
}

// AuthzHoldings is what the signed-in person holds, for the UI to show and
// hide controls. The project-creation fields are present for people only;
// project is present when one was asked about and exists.
type AuthzHoldings struct {
	User         string          `json:"user"`
	Instance     map[string]bool `json:"instance"`
	CreateIn     *[]string       `json:"createIn,omitempty"`
	DefaultGroup *string         `json:"defaultGroup,omitempty"`
	Project      map[string]bool `json:"project,omitempty"`
}

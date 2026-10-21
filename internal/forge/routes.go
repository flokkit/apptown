package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/identity"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/web"
)

// Policies are built with the registration operation that implements them.
// A requirement has no separate switch that can disable its enforcement.
type routePolicy interface {
	validate() error
	allows(*App, http.ResponseWriter, *http.Request, string) bool
}
type routeSpec struct {
	pattern string
	handler http.HandlerFunc
	policy  routePolicy
}

func (route routeSpec) validate() error {
	if route.pattern == "" || route.handler == nil || route.policy == nil {
		return fmt.Errorf("route %q requires a pattern, handler and explicit policy", route.pattern)
	}
	return route.policy.validate()
}
func requireRoute(pattern string, handler http.HandlerFunc, capability access.Capability, resolver string) routeSpec {
	return routeSpec{pattern, handler, requiredPolicy{capability: capability, resolver: resolver}}
}

// chatRoute is requireRoute for a chat: opened and driven by people and
// their own chats, never by a step, whose token is refused here whatever
// its table holds.
func chatRoute(pattern string, handler http.HandlerFunc, capability access.Capability, resolver string) routeSpec {
	return routeSpec{pattern, handler, requiredPolicy{capability: capability, resolver: resolver, chat: true}}
}

// chatActRoute is a route acting on a chat. Its owner (conversation.Chats)
// decides the act, change:invoke-lane on the chat's project, and refuses in
// the chat's own words ("Viewing only: ..."): the route's door is reading
// the project, its contract names the owner's rule, and no step acts on a chat.
func chatActRoute(pattern string, handler http.HandlerFunc, resolver, contract string) routeSpec {
	return routeSpec{pattern, handler, requiredPolicy{capability: access.CapProjectRead, resolver: resolver, chat: true, decidedBy: contract}}
}
func memberRoute(pattern string, handler http.HandlerFunc, resolver string) routeSpec {
	return routeSpec{pattern, handler, memberPolicy{resolver}}
}
func adminRoute(pattern string, handler http.HandlerFunc) routeSpec {
	return routeSpec{pattern, handler, administratorPolicy{}}
}
func publicRoute(pattern string, handler http.HandlerFunc, reason string) routeSpec {
	return routeSpec{pattern, handler, exceptionalPolicy{"public", reason}}
}
func protocolRoute(pattern string, handler http.HandlerFunc, verifier string) routeSpec {
	return routeSpec{pattern, handler, exceptionalPolicy{"protocol", verifier}}
}
func ownerRoute(pattern string, handler http.HandlerFunc, contract string) routeSpec {
	return routeSpec{pattern, handler, exceptionalPolicy{"owner", contract}}
}
func filteredRoute(pattern string, handler http.HandlerFunc, contract string) routeSpec {
	return routeSpec{pattern, handler, exceptionalPolicy{"filtered", contract}}
}

type requiredPolicy struct {
	capability access.Capability
	resolver   string
	chat       bool   // a chat's route: no step acts on it (chatRoute)
	decidedBy  string // the owner whose rule decides the act, beyond this read door (chatActRoute)
}

func (p requiredPolicy) validate() error {
	if _, ok := access.Definitions[p.capability]; !ok {
		return fmt.Errorf("unknown capability %q", p.capability)
	}
	return validateResolver(p.resolver)
}
func (p requiredPolicy) allows(a *App, w http.ResponseWriter, r *http.Request, pattern string) bool {
	if id := identityFrom(r); p.chat && id != nil && id.Via == "run" {
		writeErr(w, 403, "a step does not open or act on a chat")
		return false
	}
	resource, _, err := a.resolveRouteResource(r, p.resolver)
	if err != nil {
		writeRouteResolutionError(w, err)
		return false
	}
	// A chat's credential reaches its own chat and no other, whatever the
	// capability: its person's reads are not the chat's to borrow.
	if id := identityFrom(r); p.resolver == "console-session" && id != nil && id.Via == "console" && id.ConsoleID != r.PathValue("sid") {
		writeErr(w, 403, "a chat may only act on its own session")
		return false
	}
	_, ok := a.authorizeRequest(w, r, p.capability, resource, access.Context{Route: pattern})
	return ok
}

type memberPolicy struct{ resolver string }

func (p memberPolicy) validate() error {
	if p.resolver == "instance" {
		return fmt.Errorf("membership requires a project resolver")
	}
	return validateResolver(p.resolver)
}
func (p memberPolicy) allows(a *App, w http.ResponseWriter, r *http.Request, _ string) bool {
	_, project, err := a.resolveRouteResource(r, p.resolver)
	if err != nil {
		writeRouteResolutionError(w, err)
		return false
	}
	if id := identityFrom(r); id != nil && (id.Via == "console" || id.Via == "run") {
		if id.ProjectID != project.ID {
			writeErr(w, 403, "this session works on another project")
			return false
		}
		if sid := r.PathValue("sid"); sid != "" && id.Via == "console" && id.ConsoleID != sid {
			writeErr(w, 403, "a chat may only act on its own session")
			return false
		}
		return true
	}
	return a.requireProjectMember(w, r, project.ID, project.Slug)
}

type administratorPolicy struct{}

func (administratorPolicy) validate() error { return nil }
func (administratorPolicy) allows(a *App, w http.ResponseWriter, r *http.Request, _ string) bool {
	return a.requireAdmin(w, r)
}

// Exceptions name the actual enforcement/filtering implementation (or public
// contract). The exception completeness suite supplies executable evidence.
type exceptionalPolicy struct{ kind, contract string }

func (p exceptionalPolicy) validate() error {
	if strings.TrimSpace(p.contract) == "" {
		return fmt.Errorf("%s route requires its contract", p.kind)
	}
	switch p.kind {
	case "owner", "filtered", "protocol", "public":
		return nil
	}
	return fmt.Errorf("unknown exception %q", p.kind)
}
func (exceptionalPolicy) allows(*App, http.ResponseWriter, *http.Request, string) bool { return true }
func validateResolver(resolver string) error {
	switch resolver {
	case "instance", "project-slug", "console-session", "adr", "disposition", "change", "run", "step", "release":
		return nil
	}
	return fmt.Errorf("unknown resource resolver %q", resolver)
}

// Build privately: a malformed, duplicate or ambiguous declaration never leaves
// a partially registered mux in service. ServeMux owns HTTP pattern semantics.
func newRouteMux(a *App, routes []routeSpec) (mux *http.ServeMux, err error) {
	defer func() {
		if cause := recover(); cause != nil {
			mux = nil
			err = fmt.Errorf("invalid route table: %v", cause)
		}
	}()
	mux = http.NewServeMux()
	for _, route := range routes {
		if err := route.validate(); err != nil {
			return nil, err
		}
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(callerContext(r))
			if route.policy.allows(a, w, r, route.pattern) {
				route.handler(w, r)
			}
		})
	}
	return mux, nil
}

var errRouteID = fault.New(fault.Invalid, "bad resource id")

func writeRouteResolutionError(w http.ResponseWriter, err error) {
	httpapi.WriteFault(w, err)
}

// resolveRouteResource uses the declaration's resolver, never the URL's spelling.
// Reads must succeed before admission. The returned record is an observation,
// not a durable authorization ticket; owner commands still check mutable state.
func (a *App) resolveRouteResource(r *http.Request, resolver string) (access.Resource, *project.Project, error) {
	if resolver == "instance" {
		return a.resourceInstance(), nil, nil
	}
	ctx := r.Context()
	var p project.Project
	var res access.Resource
	var err error
	if resolver == "project-slug" || resolver == "release" {
		p, err = a.Projects.BySlug(ctx, r.PathValue("slug"))
	} else if resolver == "console-session" {
		session, e := a.Console.Get(ctx, r.PathValue("sid"))
		if e != nil {
			return res, nil, e
		}
		p, err = a.Projects.ByID(ctx, conversation.Of(session).ProjectID)
	} else {
		id, e := ids.Parse(r.PathValue("id"))
		if e != nil {
			// A change or a run may be named by its friendly form, and a
			// record a page linked before 1.0 by its former number; the
			// handler reads the id it resolves to.
			if id, e = recordByName(ctx, a.Projects, a.DB, resolver, r.PathValue("id")); e == nil {
				r.SetPathValue("id", id.String())
			} else if !errors.Is(e, errRouteID) {
				return res, nil, e
			}
		}
		if e != nil || id.IsZero() {
			return res, nil, errRouteID
		}
		var pid ids.ID
		switch resolver {
		case "change":
			ch, e := workflow.ReadChangeIn(ctx, a.DB, id)
			if e != nil {
				return res, nil, e
			}
			pid = ch.ProjectID
			res = access.Resource{Type: "change", ID: ch.ID.String(), ChangeID: ch.ID}
		case "run":
			run, e := workflow.ReadRunIn(ctx, a.DB, id)
			if e != nil {
				return res, nil, e
			}
			pid = run.ProjectID
			res = access.Resource{Type: "run", ID: run.ID.String(), RunID: run.ID, ChangeID: run.ChangeID}
		case "step":
			step, e := workflow.ReadStepIn(ctx, a.DB, id)
			if e != nil {
				return res, nil, e
			}
			run, e := workflow.ReadRunIn(ctx, a.DB, step.RunID)
			if e != nil {
				return res, nil, e
			}
			pid = run.ProjectID
			res = access.Resource{Type: "step", ID: step.ID.String(), RunID: run.ID, ChangeID: run.ChangeID}
		case "adr":
			pid, err = a.ADRs.ProjectID(ctx, id)
		case "disposition":
			d, e := a.conformance.Disposition(ctx, id)
			if e != nil {
				return res, nil, e
			}
			pid = d.ProjectID
		default:
			return res, nil, fmt.Errorf("unknown route resolver %q", resolver)
		}
		if err != nil {
			return res, nil, err
		}
		p, err = a.Projects.ByID(ctx, pid)
	}
	if err != nil {
		return res, nil, err
	}
	scope := a.resourceProject(ctx, &p)
	if res.Type != "" {
		scope.Type, scope.ID, scope.ChangeID, scope.RunID = res.Type, res.ID, res.ChangeID, res.RunID
	}
	res = scope
	if resolver == "release" {
		res = a.resourceReleaseTarget(ctx, releaseTargetName, &p)
	}
	return res, &p, nil
}

// The promotion resource remains the existing LAN/intranet target.
const releaseTargetName = "lan"

func (a *App) Handler() http.Handler {
	a.routes.once.Do(func() {
		mux, err := newRouteMux(a, appRoutes(a))
		if err != nil {
			panic(err)
		}
		a.routes.handler = web.WithEntry(a.VHost(httpapi.CLIProtocol(a.browserSecurity(a.AuthMiddleware(tracedHandler(a.telemetry, mux))))))
		if routeTableBuilt != nil {
			routeTableBuilt(a)
		}
	})
	return a.routes.handler
}

// routeTable is an App's HTTP surface, built once: building it registers
// every route's pattern, which is most of what serving a request through a
// fresh table would cost.
type routeTable struct {
	once    sync.Once
	handler http.Handler
}

// routeTableBuilt, when set, is told each time an App builds its table.
// Tests set it to hold an App to the owners its table was built with.
var routeTableBuilt func(*App)

func missingAPI(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotFound, "no such API endpoint")
}

func webHandler() http.HandlerFunc { return web.DistHandler().ServeHTTP }

// recordByName resolves what a route's {id} names when it is not an id: a
// change's or a run's friendly form ("egg-catch#12", read with the project's
// current slug), or the former number of a record whose page linked by it
// before 1.0. Anything else is not a reference to the resolver's record.
func recordByName(ctx context.Context, projects *project.Projects, db database.Queryer, resolver, ref string) (ids.ID, error) {
	if slug, number, ok := workflow.ParseChangeNumber(ref); ok && (resolver == "change" || resolver == "run") {
		p, err := projects.BySlug(ctx, slug)
		if err != nil {
			return ids.Nil, err
		}
		if resolver == "run" {
			return workflow.RunIDByNumberIn(ctx, db, p.ID, slug, number)
		}
		return workflow.ChangeIDByNumberIn(ctx, db, p.ID, slug, number)
	}
	if kind, ok := formerKinds[resolver]; ok && formerNumber(ref) {
		return formerID(ctx, db, kind, ref)
	}
	return ids.Nil, errRouteID
}

// formerKinds are the route resolvers whose records were linked by an
// integer before 1.0, and the kind their former identities are kept under.
var formerKinds = map[string]string{"change": audit.FormerChange, "run": audit.FormerRun, "step": audit.FormerStep, "adr": audit.FormerADR}

// formerNumber: a positive integer, the shape every former record id had.
func formerNumber(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == s
}

// formerID is the record this AppTown's former identity names (audit.FormerID),
// this AppTown being the one its AppTown.id setting names. An AppTown without one
// kept no former identities.
func formerID(ctx context.Context, db database.Queryer, kind, old string) (ids.ID, error) {
	value, err := identity.InstanceSettingIn(ctx, db, identity.SettingForgeID)
	if err != nil {
		return ids.Nil, err
	}
	forge, err := ids.Parse(value)
	if err != nil || forge.IsZero() {
		return ids.Nil, fault.New(fault.NotFound, "this AppTown kept no identities from before 1.0")
	}
	return audit.FormerID(ctx, db, forge, kind, old)
}

// formerPage serves a page that names a record by {id}: a former number
// this AppTown kept moves, permanently, to the same page at the record's id,
// the change page's run= and step= with it; anything else is the SPA's. The
// redirect names an id and grants nothing: reading the record needs the
// authority it always needed.
func formerPage(db database.Queryer, kind string) http.HandlerFunc {
	spa := webHandler()
	return func(w http.ResponseWriter, r *http.Request) {
		old := r.PathValue("id")
		if !formerNumber(old) {
			spa(w, r)
			return
		}
		id, err := formerID(r.Context(), db, kind, old)
		if fault.KindOf(err) == fault.NotFound {
			spa(w, r) // the page says what it could not read
			return
		}
		if err != nil {
			httpapi.WriteFault(w, err)
			return
		}
		query := r.URL.Query()
		if kind == audit.FormerChange {
			for key, keyKind := range map[string]string{"run": audit.FormerRun, "step": audit.FormerStep} {
				if v := query.Get(key); formerNumber(v) {
					moved, err := formerID(r.Context(), db, keyKind, v)
					if err != nil && fault.KindOf(err) != fault.NotFound {
						httpapi.WriteFault(w, err)
						return
					}
					if err == nil {
						query.Set(key, moved.String())
					}
				}
			}
		}
		target := strings.TrimSuffix(r.URL.Path, old) + id.String()
		if len(query) > 0 {
			target += "?" + query.Encode()
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	}
}

// byFormerID lets a route's {id} name its record by the number a page
// linked it by before 1.0, for a route with no record resolver.
func byFormerID(db database.Queryer, kind string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if old := r.PathValue("id"); formerNumber(old) {
			id, err := formerID(r.Context(), db, kind, old)
			if err != nil {
				httpapi.WriteFault(w, err)
				return
			}
			r.SetPathValue("id", id.String())
		}
		handler(w, r)
	}
}

// byNumber lets a route's {id} name the record by its number within the
// forge ("complaint 9") as well as by its id: a number is resolved through
// the owner's lookup before the handler reads the id.
func byNumber(lookup func(context.Context, int64) (ids.ID, error), handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if n, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err == nil && n > 0 {
			id, err := lookup(r.Context(), n)
			if err != nil {
				httpapi.WriteFault(w, err)
				return
			}
			r.SetPathValue("id", id.String())
		}
		handler(w, r)
	}
}

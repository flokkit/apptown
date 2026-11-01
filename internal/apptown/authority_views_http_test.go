package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/console/consoletest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestAuthorizationHTTPViewsReflectScopedGrantsAndCurrentCaller(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	h := a.Handler()
	for _, tc := range []struct {
		who string
		can bool
	}{{"owner", true}, {"outsider", false}} {
		w := authzCall(h, tc.who, "GET", "/api/v1/authz/me?project=game", "")
		var out struct {
			User              string
			Instance, Project map[string]bool
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.User != tc.who || out.Project[string(access.CapProjectIntegrations)] != tc.can || out.Instance[string(access.CapSecretReveal)] {
			t.Fatalf("capability view: %d %s %v", w.Code, w.Body.String(), err)
		}
	}
	w := authzCall(h, "boss", "GET", "/api/v1/authz/grants?scope=project:00000000-0001-7000-8000-000000000000", "")
	var out struct {
		Grants []struct {
			ID                          ids.ID
			ScopeType, ScopeID, Subject string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Grants) == 0 {
		t.Fatalf("grants: %d %s %v", w.Code, w.Body.String(), err)
	}
	for _, g := range out.Grants {
		if g.ID.IsZero() || g.ScopeType != "project" || g.ScopeID != idstest.N(1).String() || !strings.Contains(g.Subject, "owner") {
			t.Fatalf("foreign grant in scoped view: %+v", g)
		}
	}
	// Lists are projections; their authority counterparts are actual denied and
	// admitted mutations in grant_revocation_http_test and account command tests.
	for _, path := range []string{"/api/v1/users", "/api/v1/groups"} {
		w := authzCall(h, "boss", "GET", path, "")
		var rows map[string][]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || w.Code != 200 || len(rows[strings.TrimPrefix(path, "/api/v1/")]) == 0 {
			t.Fatalf("directory %s: %d %s %v", path, w.Code, w.Body.String(), err)
		}
		if strings.Contains(w.Body.String(), "passwordHash") || strings.Contains(w.Body.String(), "password_hash") {
			t.Fatal("directory exposes password hash")
		}
	}
}

func TestPrivateProjectHTTPReadRoutesEnforceDeclaredCapabilities(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker()})
	fx := fixtureWork(t, a)
	if _, err := a.Ways.PublishPack(t.Context(), "readable", "Readable", `{}`, ""); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	for _, path := range []string{
		"/api/v1/projects/game", "/api/v1/projects/game/workflows", "/api/v1/projects/game/lanes",
		"/api/v1/projects/game/manifest", "/api/v1/projects/game/pack/pull-plan?pack=readable", "/api/v1/projects/game/adoption",
		"/api/v1/projects/game/policies", "/api/v1/projects/game/services",
		"/api/v1/projects/game/guidance", "/api/v1/projects/game/integrations", "/api/v1/projects/game/targets",
		"/api/v1/projects/game/deployments/main/backups", "/api/v1/changes/" + fx.Change.String() + "/preview", "/api/v1/changes/" + fx.Change.String() + "/transcript",
	} {
		t.Run(path, func(t *testing.T) {
			w := authzCall(h, "outsider", "GET", path, "")
			if w.Code != 403 {
				t.Errorf("foreign read: %d %s", w.Code, w.Body.String())
			}
			w = authzCall(h, "owner", "GET", path, "")
			if w.Code != 200 {
				t.Errorf("owner read: %d %s", w.Code, w.Body.String())
			}
		})
	}
	// These resources exist but have no checkout in this fixture. Positive file
	// contents are checked by the repository HTTP scenario over a real checkout.
	for _, path := range []string{"/api/v1/changes/" + fx.Change.String() + "/diff", "/api/v1/runs/" + fx.Run.String() + "/diff", "/api/v1/runs/" + fx.Run.String() + "/workspace/file.txt"} {
		w := authzCall(h, "outsider", "GET", path, "")
		if w.Code != 403 {
			t.Errorf("foreign source read %s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestPrivateHTTPAdministrativeReadRoutesEnforceDeclaredCapabilities(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker()})
	if _, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "visible", Kind: deployment.KindDockerHost, Origin: deployment.OriginAttached, Status: deployment.TargetReady}); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	for _, path := range []string{
		"/api/v1/backups", "/api/v1/targets", "/api/v1/targets/visible", "/api/v1/retained", "/api/v1/infrastructure", "/api/v1/jobs",
		// Usage, tiers and departures without a project are filtered reads
		// (checkMemberScopedReads); naming another project is refused.
		"/api/v1/model-slots", "/api/v1/usage?project=game", "/api/v1/tiers?project=game", "/api/v1/guidance/generated?project=game",
		"/api/v1/projects/game/retained", "/api/v1/projects/game/members",
	} {
		t.Run(path, func(t *testing.T) {
			w := authzCall(h, "outsider", "GET", path, "")
			if w.Code != 403 {
				t.Errorf("unprivileged read: %d %s", w.Code, w.Body.String())
			}
			w = authzCall(h, "boss", "GET", path, "")
			if w.Code != 200 {
				t.Errorf("root read: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestPrivateHTTPInstitutionAndAdoptionViewsRequireTheirDeclaredAuthority(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker()})
	h := a.Handler()
	// The role catalogue is public and the policy library is a filtered read
	// (checkMemberScopedReads); these stay instance-only.
	for _, path := range []string{"/api/v1/users", "/api/v1/groups", "/api/v1/authz/grants?scope=all", "/api/v1/authz/explain?capability=project:read&project=game&user=owner", "/api/v1/integrations", "/api/v1/secrets/names", "/api/v1/complaints"} {
		t.Run(path, func(t *testing.T) {
			if w := authzCall(h, "outsider", "GET", path, ""); w.Code != 403 {
				t.Errorf("unprivileged read: %d %s", w.Code, w.Body.String())
			}
			if w := authzCall(h, "boss", "GET", path, ""); w.Code != 200 {
				t.Errorf("root read: %d %s", w.Code, w.Body.String())
			}
		})
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/complaints", `{"text":"read permission is not implied by filing"}`); w.Code != 201 {
		t.Fatalf("file complaint: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "outsider", "POST", "/api/v1/complaints/seen", ""); w.Code != 403 {
		t.Fatalf("unprivileged mark seen: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(h, "boss", "GET", "/api/v1/complaints", "")
	var before struct {
		Complaints []struct {
			State string
			Seen  bool
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil || len(before.Complaints) != 1 || before.Complaints[0].State != "new" || before.Complaints[0].Seen {
		t.Fatalf("refused command changed complaint: %s %v", w.Body.String(), err)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/complaints/seen", ""); w.Code != 200 {
		t.Fatalf("mark seen: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "boss", "GET", "/api/v1/complaints", "")
	var after struct {
		Complaints []struct {
			State string
			Seen  bool
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil || len(after.Complaints) != 1 || after.Complaints[0].State != "new" || !after.Complaints[0].Seen {
		t.Fatalf("mark seen did not persist: %s %v", w.Body.String(), err)
	}

}

// Every route that changes something states its real rule, and the router
// keeps it. Driven on real resources: a Project viewer, a chat whose person
// is that viewer, and a step token, each on the route's own project, are
// refused every capability their grants and tables do not hold. A route
// this test cannot drive fails by name, so a new one is covered or named.
//
// Kills: a member door that admits a viewer, a chat or a step before the
// owner's check; a route declaring a weaker rule than it enforces.
func TestEveryMutatingRouteRefusesWhatItsRuleDoesNotGrant(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	work := fixtureWork(t, a)
	a.addGrant("user", "viewer", access.RoleProjectViewer, "", access.ScopeProject, idstest.N(1).String(), "boss", "")
	// The fixture's everyone-is-a-release-manager grant would give the viewer
	// release:promote; this test's viewer holds Project viewer alone.
	grants, err := a.Access.GrantsAt(context.Background(), access.ScopeTarget, "*")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if _, err := a.Access.Revoke(context.Background(), g.ID, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	h := a.Handler()
	made := func(path, body, field string) string {
		t.Helper()
		w := authzCall(h, "owner", "POST", path, body)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code >= 300 {
			t.Fatalf("fixture %s: %d %s", path, w.Code, w.Body.String())
		}
		for _, key := range strings.Split(field, ".") {
			if next, ok := out[key].(map[string]any); ok {
				out = next
				continue
			}
			return fmt.Sprint(out[key])
		}
		return ""
	}
	adr := made("/api/v1/projects/game/adrs", `{"title":"a decision","bodyMd":"why"}`, "id")
	disposition := made("/api/v1/projects/game/dispositions", `{"fingerprint":"fp","status":"accepted","justification":"known"}`, "id")
	sid := made("/api/v1/projects/game/console", `{"model":"fake/model"}`, "session.id")
	values := map[string]string{"slug": "game", "number": "1", "name": "x", "capability": "x", "deployment": "main", "attempt": "1", "path": "x", "grantee": "x", "id": "1"}
	byResolver := map[string]string{"change": work.Change.String(), "run": work.Run.String(), "step": work.Gate.String(), "adr": adr, "disposition": disposition, "console-session": sid}
	mux, err := newRouteMux(a, appRoutes(a))
	if err != nil {
		t.Fatal(err)
	}
	callers := []struct {
		name string
		id   *authIdentity
		may  func(access.Capability) bool
	}{
		{"viewer", &authIdentity{User: "viewer", Via: "session"}, func(c access.Capability) bool { return access.RoleByName(access.RoleProjectViewer).Holds(c) }},
		{"chat", &authIdentity{User: "viewer via console/" + sid, Via: "console", ProjectID: idstest.N(1), ConsoleID: sid}, func(c access.Capability) bool {
			return access.InstrumentMay("console", c) && access.RoleByName(access.RoleProjectViewer).Holds(c)
		}},
		{"step", &authIdentity{User: "run/" + work.Run.String(), Role: "member", Via: "run", ProjectID: idstest.N(1)}, func(c access.Capability) bool { return access.InstrumentMay("run", c) }},
	}
	driven := 0
	for _, route := range appRoutes(a) {
		method, pattern, _ := strings.Cut(route.pattern, " ")
		if _, member := route.policy.(memberPolicy); member && method != "GET" {
			t.Errorf("%s changes something behind a member door: declare the capability its owner enforces", route.pattern)
			continue
		}
		policy, ok := route.policy.(requiredPolicy)
		if !ok || method == "GET" || policy.resolver == "instance" {
			continue
		}
		path := regexp.MustCompile(`\{([a-z]+)(\.\.\.)?\}`).ReplaceAllStringFunc(pattern, func(param string) string {
			name := strings.Trim(param, "{}.")
			if v, ok := byResolver[policy.resolver]; ok && (name == "id" || name == "sid") {
				return v
			}
			return values[name]
		})
		if strings.Contains(path, "//") || strings.HasSuffix(path, "/") {
			t.Errorf("%s: no fixture resource for its %s", route.pattern, policy.resolver)
			continue
		}
		driven++
		if policy.decidedBy != "" {
			// The owner decides the act, in its own words, beyond the read
			// door: none of these callers may act.
			for _, caller := range callers {
				if w := callAs(t, mux, caller.id, method, path, []byte(`{"text":"x","data":"data:image/png;base64,cHVibGlj"}`)); w.Code != 403 {
					t.Errorf("%s as %s (decided by %s): %d %s", route.pattern, caller.name, policy.decidedBy, w.Code, w.Body.String())
				}
			}
			continue
		}
		for _, caller := range callers {
			w := callAs(t, mux, caller.id, method, path, []byte(`{}`))
			// A chat's route admits no step, whatever the step table holds.
			switch may := caller.may(policy.capability) && !(policy.chat && caller.name == "step"); {
			case !may && w.Code != 403:
				t.Errorf("%s as %s (%s): %d %s", route.pattern, caller.name, policy.capability, w.Code, w.Body.String())
			case may && w.Code == 403:
				// The route admits this caller, so its owner must not refuse on
				// authority: a route declaring less than its owner enforces is
				// a false rule.
				t.Errorf("%s as %s: the route's %s admits, the owner refuses: %s", route.pattern, caller.name, policy.capability, w.Body.String())
			}
		}
	}
	if driven < 40 {
		t.Fatalf("only %d mutating project routes driven", driven)
	}
}

// A route's capability is its owner's, not a neighbour's that the stock
// roles happen to bundle with it: with each route's handler replaced by a
// sentinel, a person holding exactly one capability of a pair is admitted
// by the route that needs it and refused by the one that needs the other.
//
// Kills: distill gated by change:attach-evidence (its owner publishes a
// narrative), and a chat's or a change's recheck gated by change:run-checks
// (its owner requests landing).
func TestRouteGatesSeparatePairedPermissions(t *testing.T) {
	t.Parallel()
	agent := consoletest.NewAgent()
	a := workAuthorityFixture(t, Config{DockerClient: newFakeDocker(), ConsoleCLI: fixtureCLI(t), ConsoleAgent: agent,
		InferenceURL: "http://worker.test:7779", InferenceNetworks: []string{"127.0.0.1/32"}})
	configureConsoleModel(t, a)
	w := authzCall(a.Handler(), "owner", "POST", "/api/v1/projects/game/console", `{"model":"fake/model"}`)
	var opened struct{ Session struct{ ID string } }
	if err := json.Unmarshal(w.Body.Bytes(), &opened); err != nil || opened.Session.ID == "" {
		t.Fatalf("console: %d %s", w.Code, w.Body.String())
	}
	sid := opened.Session.ID
	change := fixtureWork(t, a).Change.String()
	for user, caps := range map[string][]access.Capability{
		"narrator": {access.CapProjectRead, access.CapChangeNarrative},
		"attacher": {access.CapProjectRead, access.CapChangeAttach},
		"lander":   {access.CapProjectRead, access.CapChangeRequestLand},
		"checker":  {access.CapProjectRead, access.CapChangeRunChecks},
	} {
		for _, c := range caps {
			a.addGrant("user", user, "", string(c), access.ScopeProject, idstest.N(1).String(), "boss", "")
		}
	}
	routes := appRoutes(a)
	for i := range routes {
		routes[i].handler = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }
	}
	mux, err := newRouteMux(a, routes)
	if err != nil {
		t.Fatal(err)
	}
	chat := func(person string) *authIdentity {
		return &authIdentity{User: person + " via console/" + sid, Via: "console", ProjectID: idstest.N(1), ConsoleID: sid}
	}
	for _, tc := range []struct {
		who  *authIdentity
		path string
		want int
	}{
		{&authIdentity{User: "narrator", Via: "session"}, "/api/v1/changes/" + change + "/distill", 204},
		{&authIdentity{User: "attacher", Via: "session"}, "/api/v1/changes/" + change + "/distill", 403},
		{chat("lander"), "/api/v1/console/" + sid + "/recheck", 204},
		{chat("checker"), "/api/v1/console/" + sid + "/recheck", 403},
		{&authIdentity{User: "lander", Via: "session"}, "/api/v1/changes/" + change + "/recheck", 204},
		{&authIdentity{User: "checker", Via: "session"}, "/api/v1/changes/" + change + "/recheck", 403},
	} {
		if w := callAs(t, mux, tc.who, "POST", tc.path, []byte(`{}`)); w.Code != tc.want {
			t.Errorf("%s %s: %d %s, want %d", tc.who.User, tc.path, w.Code, w.Body.String(), tc.want)
		}
	}
}

func TestAuthorizationCurrentActorUsesVerifiedIdentity(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	owner := testPrincipalOf(t, a.DB, "owner")
	outsider := testPrincipalOf(t, a.DB, "outsider")
	h := httpapi.NewAuthorization(a.Access, a.Projects)
	for _, tc := range []struct {
		name   string
		caller *access.Caller
		want   ids.ID
	}{
		{"verified", &access.Caller{Verified: true, Principal: access.Principal{Kind: "user", ID: owner, Name: "owner"}}, owner},
		{"same display different identity", &access.Caller{Verified: true, Principal: access.Principal{Kind: "user", ID: outsider, Name: "owner"}}, outsider},
		{"unverified", &access.Caller{Principal: access.Principal{Kind: "user", ID: owner, Name: "owner"}}, ids.Nil},
		{"missing", nil, ids.Nil},
		{"zero", &access.Caller{Verified: true, Principal: access.Principal{Kind: "user", Name: "owner"}}, ids.Nil},
		{"instrument", &access.Caller{Verified: true, Principal: access.Principal{Kind: "agent", ID: owner, Name: "owner", Instrument: "run"}}, ids.Nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/authz/me", nil)
			if tc.caller != nil {
				req = req.WithContext(access.WithCaller(req.Context(), *tc.caller))
			}
			out := httptest.NewRecorder()
			h.AuthzMe(out, req)
			if out.Code != http.StatusOK {
				t.Fatalf("current actor: %d %s", out.Code, out.Body.String())
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			raw, exists := got["actorId"]
			if tc.want.IsZero() {
				if exists {
					t.Fatalf("unverified or nonhuman actor exposed: %s", raw)
				}
			} else {
				var id ids.ID
				if !exists || json.Unmarshal(raw, &id) != nil || id != tc.want {
					t.Fatalf("actorId = %s; want %s", raw, tc.want)
				}
			}
		})
	}
	// Actual protocol admission agrees with the identity owned by the account.
	out := authzCall(a.Handler(), "owner", "GET", "/api/v1/authz/me", "")
	var current struct {
		ActorID ids.ID `json:"actorId"`
	}
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &current) != nil || current.ActorID != owner {
		t.Fatalf("admitted actor identity: %d %s", out.Code, out.Body.String())
	}
}

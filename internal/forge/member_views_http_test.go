package forge

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/httpapi"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/usage"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"flokkit.io/forge/internal/worker"
)

// A person whose only authority is over their own project reads every
// aggregate page with only that project in it, and is refused the pages that
// are about the instance itself. In the fixture "outsider" holds project
// roles on "open" (through everyone's group) and nothing at the instance;
// "game" is someone else's project; "boss" administers the instance.
func checkMemberScopedReads(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	for _, c := range []struct {
		who     string
		project ids.ID
		title   string
	}{{"owner", idstest.N(1), "game-secret-work"}, {"boss", idstest.N(2), "open-work"}} {
		ch, err := a.Work.Create(asPerson(c.who), workflow.ChangeDraft{ProjectID: c.project, Title: c.title})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Work.StartRun(asPerson(c.who), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "implement"}); err != nil {
			t.Fatal(err)
		}
		if err := a.Usage.Ledger.Record(context.Background(), usage.Entry{Kind: "step", ProjectID: c.project, Actor: c.who, Provider: "p", Model: "m"}, usage.Totals{Total: map[ids.ID]int64{idstest.N(1): 100, idstest.N(2): 7}[c.project]}, ""); err != nil {
			t.Fatal(err)
		}
		if err := a.Adoption.InstallPolicy(t.Context(), c.project, "shared-rule", "", "requireStepBefore", map[string]any{"uses": "scm/merged", "requires": "scan"}, "", "fixture"); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Deployment.Releases.Start(context.Background(), deployment.ReleaseDraft{Source: deployment.ReleaseFromBundle, ProjectID: c.project, Deployment: "lan", Revision: c.title, Actor: c.who}, "member views fixture"); err != nil {
			t.Fatal(err)
		}
	}
	a.Work.Wait()
	h := a.Handler()

	// Filtered reads: 200, the reader's project in, the other project out.
	for _, c := range []struct{ path, private, public string }{
		{"/api/v1/activity", "game-secret-work", "open-work"},
		{"/api/v1/tiers/departures", `"slug":"game"`, `"slug":"open"`},
		{"/api/v1/policy-library/shared-rule/usage", `"slug":"game"`, `"slug":"open"`},
		{"/api/v1/usage", `"project":"game"`, `"project":"open"`},
	} {
		for _, who := range []string{"outsider", "boss"} {
			w := authzCall(h, who, "GET", c.path, "")
			body := w.Body.String()
			if w.Code != 200 || strings.Contains(body, c.private) != (who == "boss") || !strings.Contains(body, c.public) {
				t.Errorf("%s %s: %d %s", who, c.path, w.Code, body)
			}
		}
	}
	for who, want := range map[string]int64{"outsider": 7, "boss": 107} {
		var rep struct{ Totals struct{ Total int64 } }
		w := authzCall(h, who, "GET", "/api/v1/usage", "")
		if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || rep.Totals.Total != want {
			t.Errorf("%s usage total: %d %s, want %d", who, w.Code, w.Body.String(), want)
		}
	}
	for _, path := range []string{"/api/v1/tiers", "/api/v1/policy-library", "/api/v1/guidance/generated", "/api/v1/authz/roles"} {
		if w := authzCall(h, "outsider", "GET", path, ""); w.Code != 200 {
			t.Errorf("outsider %s: %d %s", path, w.Code, w.Body.String())
		}
	}

	// A named project: the reader's own is answered, another is refused.
	for _, c := range []struct{ who, path string }{
		{"outsider", "/api/v1/usage?project=%s"},
		{"outsider", "/api/v1/tiers?project=%s"},
		{"outsider", "/api/v1/guidance/generated?project=%s"},
		{"outsider", "/api/v1/authz/explain?capability=change:approve&project=%s"},
	} {
		for slug, want := range map[string]int{"open": 200, "game": 403} {
			path := strings.Replace(c.path, "%s", slug, 1)
			if w := authzCall(h, c.who, "GET", path, ""); w.Code != want {
				t.Errorf("%s %s: %d %s, want %d", c.who, path, w.Code, w.Body.String(), want)
			}
		}
	}
	if w := authzCall(h, "owner", "GET", "/api/v1/usage?project=game", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":100`) {
		t.Errorf("owner's own usage: %d %s", w.Code, w.Body.String())
	}

	// The instance itself: refused to the project member, answered to the
	// administrator. Asking about another person is institution structure.
	for _, path := range []string{
		"/api/v1/users", "/api/v1/groups", "/api/v1/authz/grants?scope=all", "/api/v1/audit",
		"/api/v1/authz/explain?capability=change:approve", "/api/v1/authz/explain?capability=change:approve&project=open&user=boss",
		"/api/v1/infrastructure", "/api/v1/targets", "/api/v1/jobs", "/api/v1/backups", "/api/v1/retained",
		"/api/v1/integrations", "/api/v1/model-slots", "/api/v1/secrets/names", "/api/v1/complaints",
		"/api/v1/conformance", "/api/v1/conformance/reviews", "/api/v1/conformance/remediations", "/api/v1/workers", "/api/v1/runs/executing",
	} {
		if w := authzCall(h, "outsider", "GET", path, ""); w.Code != 403 {
			t.Errorf("outsider %s: %d %s, want 403", path, w.Code, w.Body.String())
		}
		if w := authzCall(h, "boss", "GET", path, ""); w.Code != 200 {
			t.Errorf("boss %s: %d %s", path, w.Code, w.Body.String())
		}
	}

	// What a restart would interrupt is answered as a list, empty or not,
	// so a script can tell "nothing" from a refusal or an error.
	var executing struct{ Executing []httpapi.ExecutingStepView }
	if w := authzCall(h, "boss", "GET", "/api/v1/runs/executing", ""); json.Unmarshal(w.Body.Bytes(), &executing) != nil || executing.Executing == nil {
		t.Errorf("executing steps: %d %s", w.Code, w.Body.String())
	}

	// The menu's answer: which instance-wide pages each person can open.
	for who, want := range map[string]bool{"outsider": false, "boss": true} {
		var me struct{ Instance map[string]bool }
		w := authzCall(h, who, "GET", "/api/v1/authz/me", "")
		if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
			t.Fatalf("%s authz/me: %d %s", who, w.Code, w.Body.String())
		}
		for _, c := range []string{"institution:read-structure", "project:read", "deployment:read", "conformance:read", "standards:manage-packs", "platform:manage-deploy-targets"} {
			if got, ok := me.Instance[c]; !ok || got != want {
				t.Errorf("%s authz/me %s = %v (reported %v), want %v", who, c, got, ok, want)
			}
		}
	}
}

// Guessing another project's address teaches nothing about it: every refusal
// speaks of "this project", never its name.
func TestRefusalsDoNotNameAProjectTheCallerCannotRead(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	// Made as the fixture makes its projects: nobody holds a role on it.
	if _, err := database.Transact(context.Background(), a.DB, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "garden", Name: "Secret Garden"})
	}); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/projects/garden"}, {"GET", "/api/v1/projects/garden/overview"}, {"GET", "/api/v1/projects/garden/members"},
		{"GET", "/api/v1/projects/garden/changes"}, {"GET", "/api/v1/usage?project=garden"}, {"GET", "/api/v1/tiers?project=garden"},
		{"GET", "/api/v1/guidance/generated?project=garden"}, {"GET", "/api/v1/authz/explain?capability=change:land&project=garden"},
		{"POST", "/api/v1/projects/garden/changes"}, {"PUT", "/api/v1/projects/garden/guidance"},
	} {
		w := authzCall(h, "outsider", c.method, c.path, "")
		if w.Code != 403 || strings.Contains(w.Body.String(), "Secret Garden") {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if w := authzCall(h, "outsider", "GET", "/api/v1/projects/garden", ""); !strings.Contains(w.Body.String(), "on this project") {
		t.Errorf("refusal says where: %s", w.Body.String())
	}
}

// The departures board selects readable projects before its
// limit, so sixty newer releases elsewhere never push out a member's own.
func TestMemberDeparturesSelectReadableProjectsBeforeTheLimit(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	releasedPreviewFixture(t, a, idstest.N(2), "fixture")
	for i := 0; i < 60; i++ {
		releasedPreviewFixture(t, a, idstest.N(1), "fixture")
	}
	w := authzCall(a.Handler(), "outsider", "GET", "/api/v1/tiers/departures", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"slug":"open"`) || strings.Contains(w.Body.String(), `"slug":"game"`) {
		t.Fatalf("own release lost behind other projects: %d %s", w.Code, w.Body.String())
	}
}

// A run or a chat bound to one project reads that project's
// usage only, even when its person could read the whole instance.
func TestUsageKeepsARunOrChatToItsProject(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	for _, row := range []struct {
		pid   ids.ID
		total int64
	}{{idstest.N(1), 100}, {idstest.N(2), 7}, {ids.Nil, 1000}} {
		if err := a.Usage.Ledger.Record(context.Background(), usage.Entry{Kind: "step", ProjectID: row.pid, Actor: "owner"}, usage.Totals{Total: row.total}, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, pr := range []access.Principal{
		{Kind: "instrument", Name: "run/1", Instrument: "run", ProjectID: idstest.N(2), Via: "run"},
		{Kind: "instrument", Name: "boss via console/c", Instrument: "console", Person: "boss", ProjectID: idstest.N(2), Via: "console", Role: "member"},
	} {
		ctx := access.WithCaller(context.Background(), access.Caller{Principal: pr, Verified: true, Receipt: pr.Name})
		w := httptest.NewRecorder()
		httpapi.NewUsage(a.Usage, a.Projects).Usage(w, httptest.NewRequest("GET", "/api/v1/usage", nil).WithContext(ctx))
		var rep struct{ Totals struct{ Total int64 } }
		if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || rep.Totals.Total != 7 {
			t.Errorf("%s escaped project 2: %d %s", pr.Name, w.Code, w.Body.String())
		}
	}
}

// Reading a change is not reading its project; the tier map
// shows a live preview only in a project the caller can read.
func TestTierPreviewsNeedTheProjectRead(t *testing.T) {
	t.Parallel()
	a, _, fake, ch := previewWorkerWorld(t, false)
	a.addGrant("user", "outsider", "", string(access.CapChangeRead), access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
	fake.mu.Lock()
	fake.running = map[string]worker.PreviewState{"forge-preview-change-" + ch.ID.String(): {Running: true}}
	fake.mu.Unlock()
	if w := authzCall(a.Handler(), "outsider", "GET", "/api/v1/projects/game", ""); w.Code != 403 {
		t.Fatalf("fixture project readable: %d", w.Code)
	}
	if w := authzCall(a.Handler(), "boss", "GET", "/api/v1/tiers", ""); w.Code != 200 || !strings.Contains(w.Body.String(), ch.Title) {
		t.Fatalf("the preview is not live for a reader: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(a.Handler(), "outsider", "GET", "/api/v1/tiers", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), ch.Title) {
		t.Fatalf("preview from unreadable project: %d %s", w.Code, w.Body.String())
	}
}

// A departures row needs both the project and its releases to
// be readable, and both are selected before the limit. Reading releases on
// a project you cannot otherwise read never crowds out your own.
func TestMemberDeparturesIntersectReadsBeforeTheLimit(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	releasedPreviewFixture(t, a, idstest.N(2), "fixture")
	for i := 0; i < 60; i++ {
		releasedPreviewFixture(t, a, idstest.N(1), "fixture")
	}
	a.addGrant("user", "outsider", "", string(access.CapReleaseRead), access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
	if w := authzCall(a.Handler(), "outsider", "GET", "/api/v1/projects/game", ""); w.Code != 403 {
		t.Fatalf("fixture project readable: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(a.Handler(), "outsider", "GET", "/api/v1/tiers/departures", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"slug":"open"`) || strings.Contains(w.Body.String(), `"slug":"game"`) {
		t.Fatalf("own release lost behind release-readable but project-unreadable rows: %d %s", w.Code, w.Body.String())
	}
}

// The other half of the intersection: reading a project without its
// releases shows none of that project's departures.
func TestMemberDeparturesNeedTheReleaseRead(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	releasedPreviewFixture(t, a, idstest.N(1), "fixture")
	a.addGrant("user", "outsider", "", string(access.CapProjectRead), access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "")
	if w := authzCall(a.Handler(), "outsider", "GET", "/api/v1/projects/game", ""); w.Code != 200 {
		t.Fatalf("fixture project unreadable: %d %s", w.Code, w.Body.String())
	}
	w := authzCall(a.Handler(), "outsider", "GET", "/api/v1/tiers/departures", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"slug":"game"`) {
		t.Fatalf("releases shown without release read: %d %s", w.Code, w.Body.String())
	}
}

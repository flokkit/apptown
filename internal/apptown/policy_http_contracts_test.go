package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/wow"

	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adrs"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func checkCreationPolicy(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	h := a.Handler()
	body := `{"slug":"policy-created","name":"Policy created"}`
	if w := authzCall(h, "outsider", "POST", "/api/v1/projects", body); w.Code != 403 {
		t.Fatalf("denial: %d %s", w.Code, w.Body.String())
	}
	if p, err := a.Projects.BySlug(context.Background(), "policy-created"); err == nil {
		t.Fatalf("denied creation persisted: %+v", p)
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/projects", body); w.Code != 201 {
		t.Fatalf("admission: %d %s", w.Code, w.Body.String())
	}
	if p, err := a.Projects.BySlug(context.Background(), "policy-created"); err != nil || p.Name != "Policy created" || p.OwnerGroup != "boss" {
		t.Fatalf("created project: %+v %v", p, err)
	}
}

func checkFilteredHTTPPolicies(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	ch, err := a.Work.Create(asPerson("boss"), workflow.ChangeDraft{ProjectID: idstest.N(2), Title: "public-work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.StartRun(asPerson("boss"), workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "implement"}); err != nil {
		t.Fatal(err)
	}
	a.Work.Wait()
	decisions := a.ADRs
	var private ids.ID
	for _, p := range []struct {
		id    ids.ID
		title string
	}{{idstest.N(1), "private-decision"}, {idstest.N(2), "public-decision"}} {
		rec, err := decisions.Create(asPerson("boss"), adrs.Create{ProjectID: p.id, Title: p.title, BodyMD: p.title})
		if err != nil {
			t.Fatal(err)
		}
		if p.id == idstest.N(1) {
			private = rec.ID
		}
	}
	_, err = a.Ways.PublishPack(t.Context(), "visible-pack", "Public pack metadata", `{}`, "")
	if err != nil {
		t.Fatal(err)
	}
	pack, err := a.Ways.Pack(t.Context(), "visible-pack")
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range []ids.ID{idstest.N(1), idstest.N(2)} {
		if _, err := a.lifecycle.ApplyPack(asPerson("boss"), a.projectByID(context.Background(), pid), []*wow.Pack{pack}, "boss"); err != nil {
			t.Fatal(err)
		}
	}
	h := a.Handler()
	for _, path := range []string{"/api/v1/projects", "/api/v1/dashboard", "/api/v1/adrs"} {
		for _, who := range []string{"outsider", "boss"} {
			w := authzCall(h, who, "GET", path, "")
			if w.Code != 200 {
				t.Fatalf("%s filtered aggregate: %d %s", who, w.Code, w.Body.String())
			}
			private, public := "private-decision", "public-decision"
			if path == "/api/v1/projects" {
				private, public = `"slug":"game"`, `"slug":"open"`
			}
			if strings.Contains(w.Body.String(), private) != (who == "boss") || !strings.Contains(w.Body.String(), public) {
				t.Fatalf("%s %s visibility: %s", who, path, w.Body.String())
			}
		}
	}
	for _, who := range []string{"outsider", "boss"} {
		wantPrivate := who == "boss"
		for _, c := range []struct{ path, priv, pub string }{

			{"/api/v1/runs", `"projectSlug":"game"`, `"projectSlug":"open"`},
			{"/api/v1/wow", `"game"`, `"open"`},
		} {
			w := authzCall(h, who, "GET", c.path, "")
			if w.Code != 200 || strings.Contains(w.Body.String(), c.priv) != wantPrivate || !strings.Contains(w.Body.String(), c.pub) {
				t.Errorf("%s %s visibility: %d %s", who, c.path, w.Code, w.Body.String())
			}
		}
		w := authzCall(h, who, "GET", "/api/v1/timing", "")
		var got struct{ Workflows []struct{ Runs int } }
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
			t.Fatalf("timing: %d %s", w.Code, w.Body.String())
		}
		count := 0
		for _, wf := range got.Workflows {
			count += wf.Runs
		}
		want := 1
		if wantPrivate {
			want = 2
		}
		if count != want {
			t.Errorf("%s timing exposed %d runs, want %d", who, count, want)
		}
		w = authzCall(h, who, "GET", fmt.Sprintf("/api/v1/adrs/%d", private), "")
		status := 403
		if wantPrivate {
			status = 200
		}
		if w.Code != status || strings.Contains(w.Body.String(), "private-decision") != wantPrivate {
			t.Errorf("%s ADR: %d %s", who, w.Code, w.Body.String())
		}
	}
}

func checkConformancePolicies(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	pid, _, _ := fleetRepo(t, a, "private-evidence", map[string]string{"README.md": "private source"})
	adopt(t, a, pid, "policy-check", 1, checksJSON(llmCheck("private-check")))
	ctx := asPerson("boss")
	review, err := a.conformance.BeginReview(ctx, conformance.ReviewRequest{Projects: []ids.ID{pid}, Trigger: "policy-evidence"})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := review.Projects(ctx)
	if err != nil || len(projects) != 1 || len(projects[0].Checks) != 1 {
		t.Fatalf("review plan: %+v %v", projects, err)
	}
	check := projects[0].Checks[0]
	pending, err := review.PrepareVerdict(ctx, conformance.Assessment{ProjectID: pid, Check: conformance.CheckIdentity{ProjectID: pid, Block: check.Block, Version: check.Version, Check: check.Check.Name}, State: conformance.DoesNotConform, Detail: "private verdict detail", EvaluatorVersion: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := pending.Commit(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := review.Finish(ctx, conformance.Completion{Projects: 1, ProjectsDone: 1}, conformance.ReviewDone)
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	for _, path := range []string{"/api/v1/conformance", "/api/v1/conformance/reviews", fmt.Sprintf("/api/v1/conformance/reviews/%d", saved.ID)} {
		w := authzCall(h, "outsider", "GET", path, "")
		if w.Code != 403 || strings.Contains(w.Body.String(), "private-") {
			t.Fatalf("private review leaked: %d %s", w.Code, w.Body.String())
		}
		w = authzCall(h, "boss", "GET", path, "")
		if w.Code != 200 || (!strings.Contains(w.Body.String(), "private-") && !strings.Contains(w.Body.String(), "policy-evidence")) {
			t.Fatalf("authorized review absent: %d %s", w.Code, w.Body.String())
		}
	}
	path := fmt.Sprintf("/api/v1/conformance/verdicts/%d/confirm", v.ID)
	if w := authzCall(h, "outsider", "POST", path, `{"note":"untrusted"}`); w.Code != 403 {
		t.Fatalf("confirmation denial: %d %s", w.Code, w.Body.String())
	}
	view, err := a.conformance.View(ctx, saved.ID)
	if err != nil || len(view.Adjudications) != 0 {
		t.Fatalf("denial confirmed: %+v %v", view, err)
	}
	if w := authzCall(h, "boss", "POST", path, `{"note":"checked"}`); w.Code != 201 {
		t.Fatalf("confirmation: %d %s", w.Code, w.Body.String())
	}
	view, err = a.conformance.View(ctx, saved.ID)
	if err != nil || view.Adjudications[v.ID].By != "boss" {
		t.Fatalf("missing confirmation: %+v %v", view, err)
	}
}

func policyConnection(t *testing.T, a *App, who string) ids.ID {
	t.Helper()
	client, err := a.Access.RegisterOAuthClient(context.Background(), identity.Registration{Name: "policy-client-" + who, RedirectURIs: []string{"http://127.0.0.1:9999/callback"}})
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge := pkce()
	browser := identity.BrowserAuthorization{User: who, Binding: "session", Request: identity.AuthorizationRequest{ClientID: client.ID, RedirectURI: "http://127.0.0.1:9999/callback", Scope: "apptown:user:read", Challenge: challenge, Method: "S256", Resource: identity.AudienceUser}}
	prepared, err := a.Access.PrepareConsent(context.Background(), browser)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Access.DecideConsent(context.Background(), identity.ConsentDecision{Browser: browser, Token: prepared.Token, Choice: identity.ConsentAllow})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Access.ExchangeCode(context.Background(), identity.CodeExchange{Code: result.Code, Verifier: verifier, ClientID: client.ID, RedirectURI: browser.Request.RedirectURI, AuthMethod: "internal"}); err != nil {
		t.Fatal(err)
	}
	rows, err := a.Access.Connections(context.Background(), who, false)
	if err != nil || len(rows) != 1 {
		t.Fatalf("connection: %+v %v", rows, err)
	}
	return rows[0].ID
}
func checkDelegationPolicies(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	own := policyConnection(t, a, "owner")
	foreign := policyConnection(t, a, "outsider")
	h := a.Handler()
	w := authzCall(h, "owner", "GET", "/api/v1/auth/delegations?all=1", "")
	var out struct{ Delegations []identity.Delegation }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Delegations) != 1 || out.Delegations[0].ID != own {
		t.Fatalf("filtered connections: %d %s", w.Code, w.Body.String())
	}
	path := fmt.Sprintf("/api/v1/auth/delegations/%d", foreign)
	if w := authzCall(h, "owner", "DELETE", path, ""); w.Code != 403 {
		t.Fatalf("foreign revocation: %d %s", w.Code, w.Body.String())
	}
	d, err := a.Access.Connection(context.Background(), foreign)
	if err != nil || !d.LiveAt(time.Now()) {
		t.Fatalf("denial revoked: %+v %v", d, err)
	}
	if w := authzCall(h, "outsider", "DELETE", path, ""); w.Code != 200 {
		t.Fatalf("own revocation: %d %s", w.Code, w.Body.String())
	}
	d, err = a.Access.Connection(context.Background(), foreign)
	if err != nil || d.LiveAt(time.Now()) {
		t.Fatalf("revocation missing: %+v %v", d, err)
	}
	d, err = a.Access.Connection(context.Background(), own)
	if err != nil || !d.LiveAt(time.Now()) {
		t.Fatalf("unrelated connection revoked: %+v %v", d, err)
	}
}

func TestRemediationRoutesRequireInstanceAuthorityBeforeEffects(t *testing.T) {
	t.Parallel()
	a := workAuthorityFixture(t)
	h := a.Handler()
	// Even a fleet reader cannot start fixes; a project member cannot read the
	// unfiltered fleet remediation ledger.
	a.addGrant("user", "kid", "", string(access.CapConformanceRead), access.ScopeInstance, "", "boss", "")
	for _, c := range []struct{ who, method, path, body string }{{"outsider", "GET", "/api/v1/conformance/remediations", ""}, {"outsider", "GET", "/api/v1/conformance/remediations/9999", ""}, {"kid", "POST", "/api/v1/conformance/remediations", `{"scope":"all"}`}} {
		if w := authzCall(h, c.who, c.method, c.path, c.body); w.Code != 403 {
			t.Fatalf("missing authority: %d %s", w.Code, w.Body.String())
		}
	}
	jobs, err := a.Fixes.Remediations(context.Background(), 50)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("denial admitted work: %+v %v", jobs, err)
	}
	if w := authzCall(h, "kid", "GET", "/api/v1/conformance/remediations", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"remediations":[]`) {
		t.Fatalf("permitted reader: %d %s", w.Code, w.Body.String())
	}
	// Positive command admission and returned item scope are covered by the
	// existing TestRemediationHTTPPreviewControlsAndItemScope scenario.
}

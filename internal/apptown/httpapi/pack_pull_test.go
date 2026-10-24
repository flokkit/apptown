package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/stock"
)

const httpPackWorkflowYAML = `
apiVersion: apptown.dev/v1
kind: Workflow
metadata: { name: flow }
spec:
  kind: lifecycle
  stages:
    - name: decide
      steps:
        - name: first-ok
          uses: approval
          with: { approvers: [bls], means: "the direction is right" }
    - name: confirm
      steps:
        - name: second-ok
          uses: approval
          with: { approvers: [bls], means: "the result is right" }
`

type packPullFixture struct {
	db        *database.DB
	ways      *wow.Service
	adoption  *adoption.Service
	projects  *project.Projects
	identity  *identity.Service
	projectID ids.ID
	ctx       context.Context
}

func newPackPullFixture(t *testing.T) packPullFixture {
	t.Helper()
	db := databaseSuite.New(t)
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, accounts)
	pid, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "p", Name: "P"})
	})
	if err != nil {
		t.Fatal(err)
	}
	ways := wow.New(db, wow.Dependencies{Stock: stock.FS, ToolLines: stock.Versions()})
	adopt := adoption.New(db, adoption.Dependencies{Ways: ways})
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "test"}, Receipt: "bls", Verified: true})
	return packPullFixture{db: db, ways: ways, adoption: adopt, projects: projects, identity: accounts, projectID: pid, ctx: ctx}
}

func (f packPullFixture) publishWorkflow(t *testing.T, name string, deprecated bool) {
	t.Helper()
	yaml := strings.Replace(httpPackWorkflowYAML, "metadata: { name: flow }", "metadata: { name: "+name+" }", 1)
	if deprecated {
		yaml = strings.Replace(yaml, "metadata: { name: "+name+" }", "metadata: { name: "+name+", deprecated: true }", 1)
	}
	got, version, err := f.ways.PublishWorkflow(t.Context(), yaml, "fixture")
	if err != nil || got != name || version != 1 {
		t.Fatalf("published workflow = %s@%d, want %s@1: %v", got, version, name, err)
	}
}

func (f packPullFixture) publishPack(t *testing.T, name string, workflows []string) *wow.Pack {
	t.Helper()
	spec, err := json.Marshal(map[string]any{"name": name, "workflows": workflows})
	if err != nil {
		t.Fatal(err)
	}
	if version, err := f.ways.PublishPack(t.Context(), name, "", string(spec), "stock"); err != nil || version != 1 {
		t.Fatalf("publish pack %s: version=%d err=%v", name, version, err)
	}
	pack, err := f.ways.Pack(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func TestPackPull(t *testing.T) {
	t.Parallel()
	f := newPackPullFixture(t)
	for _, lane := range []struct {
		name       string
		deprecated bool
	}{{"new-lane", false}, {"old-lane", false}, {"custom-lane", false}, {"dead-lane", true}} {
		f.publishWorkflow(t, lane.name, lane.deprecated)
	}
	for _, name := range []string{"old-lane", "custom-lane"} {
		if err := f.adoption.SelectLane(t.Context(), f.projectID, name, true, "alice"); err != nil {
			t.Fatal(err)
		}
	}
	pack := f.publishPack(t, "test-pack", []string{"new-lane", "dead-lane"})
	plan, err := f.adoption.PackPullPlan(t.Context(), f.projectID, *pack)
	if err != nil || len(plan.Additions) != 1 || plan.Additions[0] != "new-lane" {
		t.Fatalf("additions: %v err=%v", plan.Additions, err)
	}
	if len(plan.Removals) != 2 || plan.Removals[0] != "custom-lane" || plan.Removals[1] != "old-lane" {
		t.Fatalf("removals (selections the pack dropped, sorted): %v", plan.Removals)
	}
	if len(plan.Deprecated) != 1 || plan.Deprecated[0] != "dead-lane" {
		t.Fatalf("deprecated: %v", plan.Deprecated)
	}
	handler := NewPackPull(f.adoption, f.projects, f.identity, f.ways, f.db, nil)
	req := httptest.NewRequest("POST", "/api/v1/projects/p/pack/pull", strings.NewReader(`{"pack":"test-pack"}`)).WithContext(f.ctx)
	req.SetPathValue("slug", "p")
	w := httptest.NewRecorder()
	handler.PackPull(w, req)
	if w.Code != 200 {
		t.Fatalf("pull: %d %s", w.Code, w.Body.String())
	}
	names := map[string]bool{}
	available, err := f.adoption.AvailableWorkflows(req.Context(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range available {
		names[lane.Name] = true
	}
	if !names["new-lane"] || names["old-lane"] || names["custom-lane"] || names["dead-lane"] {
		t.Fatalf("post-pull selection wrong: %v", names)
	}
	recorded, err := f.adoption.Current(req.Context(), f.projectID)
	if err != nil || recorded.Pack != "test-pack" {
		t.Fatalf("project must record its pack: %+v %v (pull said: %s)", recorded, err, w.Body.String())
	}
	if audits := receiptsOfKind(t, f.db, "pack.pulled"); len(audits) != 1 {
		t.Fatalf("pull must be audited: %+v", audits)
	}
}

package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestProjectIntegrationReadBatchesAndContainsPrivateRecords(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	projects := project.New(db, nil)
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	ctx := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "bls", Via: "session"}, Verified: true})
	work := workflow.New(context.Background(), db, workflow.Dependencies{Authority: access.Checked{Authority: accounts, Projects: projects}})
	t.Cleanup(work.Stop)
	integrations := integration.NewService(db, integration.Dependencies{Projects: projects})
	handler := NewProjectAttachments(projects, integrations, accounts, changeactions.New(work, db), db, "", nil)
	var projectIDs, changeIDs []ids.ID
	for _, spec := range []struct{ slug, name string }{{"shop", "Shop"}, {"other", "Other"}} {
		projectIDs = append(projectIDs, httpTestProject(t, db, spec.slug, spec.name))
	}
	for i, title := range []string{"Own change", "Private other change"} {
		ch, err := work.Create(ctx, workflow.ChangeDraft{ProjectID: projectIDs[i], Type: "change", Title: title})
		if err != nil {
			t.Fatalf("projection fixture change: %+v %v", ch, err)
		}
		changeIDs = append(changeIDs, ch.ID)
	}
	if _, err := integrations.Bindings.Save(ctx, "mail", "mailer", "service", `{"host":"smtp.internal","private":"connector-canary"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := integrations.ServiceKinds.Save(ctx, "mailer", "capability: mailer\ntitle: Mail Relay", "Mail Relay", "fixture"); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: The joined projection requires a historical attachment timestamp and foreign/missing wire Change IDs; Attach stamps now and does not expose wire_change_id, and the generated insert is owner-private.
	if _, err := db.Exec(`INSERT INTO project_integrations(project_id,capability,binding,resource_json,provenance,wire_change_id,created_at) VALUES
 (?,'mailer','mail','{"domain":"shop.invalid"}','service-editor',?,'2020-01-02T03:04:05.123456+11:00'),
 (?,'errors','mail','{"dsn":"dsn-canary"}','direct',?,now()),
 (?,'feedback','gone','{"appKey":"app-key-canary"}','direct',?,now())`, projectIDs[0], changeIDs[0], projectIDs[0], changeIDs[1], projectIDs[0], idstest.N(999)); err != nil {
		t.Fatal(err)
	}
	if err := integrations.Attachments.Attach(ctx, projectIDs[0], "container-registry", "gone", `{"token":"registry-canary"}`, integration.Provenance("direct"), "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if err := integrations.Attachments.Remember(ctx, projectIDs[0], "scm", "gitlab", `{"token":"scm-canary"}`); err != nil {
		t.Fatal(err)
	}
	counted := &countedQueries{Handle: db.SQL()}
	rows, err := integration.AttachedRows(ctx, counted, projectIDs[0])
	if err != nil || len(rows) != 4 || counted.reads != 1 {
		t.Fatalf("attachments: %d reads, %d rows: %v", counted.reads, len(rows), err)
	}
	for _, p := range rows {
		if p.Capability == "mailer" {
			if p.WireChangeID != changeIDs[0] || !strings.Contains(p.KindYAML, "title: Mail Relay") || !strings.Contains(p.ConfigJSON, "smtp.internal") || p.CreatedAt.UTC().Format(time.RFC3339Nano) != "2020-01-01T16:04:05.123456Z" {
				t.Fatalf("incomplete attached service: %+v", p)
			}
		} else if p.ConfigJSON != "null" || !p.WireChangeID.IsZero() {
			t.Fatalf("cross-capability or cross-project join: %+v", p)
		}
	}
	for i := range 12 {
		if err := integrations.Attachments.Attach(ctx, projectIDs[0], fmt.Sprintf("extra-%02d", i), "gone", "{}", integration.Provenance("direct"), "fixture", nil); err != nil {
			t.Fatal(err)
		}
	}
	counted.reads = 0
	if rows, err := integration.AttachedRows(ctx, counted, projectIDs[0]); err != nil || len(rows) != 16 || counted.reads != 1 {
		t.Fatalf("query growth: %d reads, %d rows: %v", counted.reads, len(rows), err)
	}
	// The rendering path consumes the joined records, including service definitions.
	if services := integrations.AttachedServices(ctx, projectIDs[0]); len(services) != 1 || services[0].Config["host"] != "smtp.internal" {
		t.Fatalf("render inputs: %+v", services)
	}

	w := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/v1/projects/shop/integrations", nil).WithContext(ctx)
	request.SetPathValue("slug", "shop")
	handler.ListProjectIntegrations(w, request)
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{"connector-canary", "dsn-canary", "app-key-canary", "registry-canary", "scm-canary", "Private other change"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("private data reached attachment response: %s", secret)
		}
	}
	if !strings.Contains(w.Body.String(), "Own change") || !strings.Contains(w.Body.String(), "2020-01-01T16:04:05.123456Z") {
		t.Fatalf("lost wire/time: %s", w.Body.String())
	}
	raw, err := integration.Resource(ctx, db, projectIDs[0], "scm")
	if err != nil || !strings.Contains(raw, "scm-canary") {
		t.Fatal("private token resource unavailable to engine")
	}
	t.Cleanup(db.FailNextStatement("FROM project_integrations p"))
	w = httptest.NewRecorder()
	handler.ListProjectIntegrations(w, request)
	if w.Code != 500 {
		t.Fatalf("failed list became partial success: %d", w.Code)
	}
	t.Cleanup(db.FailNextStatement("-- name: ProjectIntegration :one"))
	if _, err := integration.Resource(ctx, db, projectIDs[0], "scm"); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("resource failure became absence: %v", err)
	}
}

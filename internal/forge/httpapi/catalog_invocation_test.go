package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/changeactions"
	"flokkit.io/forge/internal/forge/conversation"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
)

func TestCatalogInvocationHTTPUsesNamedVersionAndParameters(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"project API", "console"} {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowHTTPFixture(t, true)
			caller, _ := access.CallerFrom(f.Context)
			caller.Receipt = "bls"
			f.Context = access.WithCaller(f.Context, caller)
			ways := wow.New(f.DB, wow.Dependencies{})
			catalog, err := ways.CreateCatalogChange(f.Context, "repair/controls", "Repair input", wow.CatalogDef{Workflow: "old-workflow", Title: "Old title", Prompt: "Old brief"}, "bls")
			if err != nil {
				t.Fatal(err)
			}
			if err := ways.BumpCatalogChange(f.Context, catalog.ID, catalog.Name, wow.CatalogDef{
				Workflow: "repair", Title: "Fix {{part}}", Prompt: "Repair {{part}} for {{device}}.", Type: "fix", Model: "fake/catalog-model",
				Params: []wow.CatalogParam{{Name: "part", Required: true}, {Name: "device", Default: "keyboard"}},
			}, "bls"); err != nil {
				t.Fatal(err)
			}
			actions := changeactions.New(f.Work, ways, f.DB)
			web := NewCatalogChanges(ways, nil, f.Projects, f.Access, actions, "open")
			call := func(name, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(f.Context)
				r.SetPathValue("name", name)
				r.Header.Set("X-AppTown-Actor", "apptownd-operator")
				w := httptest.NewRecorder()
				web.CatalogApply(w, r)
				return w
			}
			var started func() []ids.ID
			if surface == "console" {
				sessions, _, session, _ := consoleHTTPFixture(t, map[string]string{conversation.ProjectLabel: fmt.Sprint(f.ProjectID), conversation.SlugLabel: "shop"})
				h := NewConsoleInvocation(sessions, f.Projects, ways, f.Work, actions, f.DB, "forge.example", nil)
				started = func() []ids.ID { return conversation.Started(session) }
				call = func(name, body string) *httptest.ResponseRecorder {
					// The same parameter payload is sent through the console's
					// catalog selector rather than the API's path selector.
					body = fmt.Sprintf(`{"catalog":%q,`, name) + strings.TrimPrefix(body, "{")
					r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(f.Context)
					r.SetPathValue("sid", session.ID)
					r.Header.Set("X-AppTown-Session-Token", session.Token)
					w := httptest.NewRecorder()
					h.ConsoleInvoke(w, r)
					return w
				}
			}
			for _, tc := range []struct {
				name, body string
				status     int
			}{
				{"missing", `{"project":"shop","params":{"part":"controls"}}`, 404},
				{catalog.Name, `{"project":"shop","params":`, 400},
				{catalog.Name, `{"project":"shop","params":{"part":42}}`, 400},
				{catalog.Name, `{"project":"shop","params":{}}`, 422},
			} {
				want := tc.status
				if surface == "console" && want == 422 {
					want = 400
				}
				w := call(tc.name, tc.body)
				if w.Code != want {
					t.Fatalf("rejected catalog request: %d %s", w.Code, w.Body.String())
				}
				changes, err := f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
				if err != nil || len(changes) != 1 || changes[0].ID != f.ChangeID {
					t.Fatalf("rejected catalog admitted work: %+v %v", changes, err)
				}
				if started != nil && len(started()) != 0 {
					t.Fatal("rejected catalog attached work to chat")
				}
			}
			w := call(catalog.Name, `{"project":"shop","params":{"part":"arrow keys"},"workflow":"payload-must-not-pick-this","title":"payload title"}`)
			var out struct {
				Change                      workflow.Change
				Run                         workflow.Run
				Catalog, CatalogStatus, URL string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 || out.Change.ID == f.ChangeID || out.Change.ID.IsZero() || out.Change.ProjectID != f.ProjectID || out.Change.Type != "fix" || out.Change.Title != "Fix arrow keys" || out.Change.Body != "Repair arrow keys for keyboard." || out.Run.ID.IsZero() || out.Run.ChangeID != out.Change.ID || out.Run.WorkflowName != "repair" {
				t.Fatalf("catalog admission: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			if surface == "project API" && (out.Catalog != catalog.Name || out.CatalogStatus != "draft") {
				t.Fatalf("draft catalog response: %+v", out)
			}
			if started != nil && (len(started()) != 1 || started()[0] != out.Change.ID || out.URL != fmt.Sprintf("https://forge.example/changes/%d", out.Change.ID)) {
				t.Fatalf("console lost admitted work/link: %+v %v", out, started())
			}
			snapshot, err := f.Work.Snapshot(f.Context, out.Run.ID, workflow.DetailView)
			if err != nil {
				t.Fatal(err)
			}
			var inputs struct {
				Catalog struct {
					Name, Status, Model string
					Applied             map[string]string
				}
			}
			if err := json.Unmarshal([]byte(snapshot.Run.InputsJSON), &inputs); err != nil || inputs.Catalog.Name != "repair/controls@2" || inputs.Catalog.Status != "draft" || inputs.Catalog.Model != "fake/catalog-model" || inputs.Catalog.Applied["part"] != "arrow keys" {
				t.Fatalf("pinned catalog input: %s (%v)", snapshot.Run.InputsJSON, err)
			}
			var receipts int
			for _, event := range snapshot.Events {
				if event.Kind != "catalog.applied" {
					continue
				}
				receipts++
				var receipt struct {
					Catalog, By string
					Version     int
				}
				if err := json.Unmarshal(event.Payload, &receipt); err != nil || receipt.Catalog != catalog.Name || receipt.Version != 2 || receipt.By != "bls" {
					t.Fatalf("catalog receipt: %s (%v)", event.Payload, err)
				}
			}
			if receipts != 1 {
				t.Fatalf("catalog receipts: %d", receipts)
			}
		})
	}
}

func TestCatalogApplyHTTPKeepsProjectMembershipAndCaller(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPAuthorityFixture(t)
	ways := wow.New(f.DB, wow.Dependencies{})
	catalog, err := ways.CreateCatalogChange(f.Context, "repair", "Repair", wow.CatalogDef{Workflow: "test", Title: "Repair controls", Prompt: "Keep existing behavior."}, "bls")
	if err != nil {
		t.Fatal(err)
	}
	h := NewCatalogChanges(ways, nil, f.Projects, f.Access, changeactions.New(f.Work, ways, f.DB), "open")
	call := func(who, project string) *httptest.ResponseRecorder {
		ctx := access.WithCaller(f.Context, access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: "session"}, Receipt: who, Verified: true})
		r := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"project":%q}`, project))).WithContext(ctx)
		r.SetPathValue("name", catalog.Name)
		r.Header.Set("X-AppTown-Actor", "bls")
		w := httptest.NewRecorder()
		h.CatalogApply(w, r)
		return w
	}
	for _, tc := range []struct {
		who, project string
		status       int
	}{{"owner", "missing", 404}, {"outsider", "shop", 403}} {
		w := call(tc.who, tc.project)
		if w.Code != tc.status {
			t.Fatalf("catalog project selection: %d %s", w.Code, w.Body.String())
		}
		changes, err := f.Work.Changes(f.Context, f.ProjectID, workflow.ChangeList{})
		if err != nil || len(changes) != 1 {
			t.Fatalf("rejected catalog opened work: %+v %v", changes, err)
		}
	}
	w := call("owner", "shop")
	var out struct {
		Change workflow.Change
		Run    workflow.Run
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 201 || out.Change.ProjectID != f.ProjectID || out.Run.ChangeID != out.Change.ID {
		t.Fatalf("member apply: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	snapshot, err := f.Work.Snapshot(f.Context, out.Run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var receipts int
	for _, event := range snapshot.Events {
		if event.Kind != "catalog.applied" {
			continue
		}
		receipts++
		var receipt struct{ By string }
		if err := json.Unmarshal(event.Payload, &receipt); err != nil || receipt.By != "owner" {
			t.Fatalf("apptownd attribution: %s (%v)", event.Payload, err)
		}
	}
	if receipts != 1 {
		t.Fatalf("member receipt count: %d", receipts)
	}
}

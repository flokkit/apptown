package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adrs"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

func adrHTTPFixture(t *testing.T) (*database.DB, *adrs.Service, *project.Projects, ids.ID) {
	t.Helper()
	db := databaseSuite.New(t)
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "bls"); err != nil {
		t.Fatal(err)
	}
	projects := project.New(db, nil)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "proj", Name: "Proj"})
	})
	if err != nil {
		t.Fatal(err)
	}
	service := adrs.New(db, access.Checked{Authority: identity.Scoped(accounts, "adrs"), Projects: projects}, nil, nil)
	return db, service, projects, id
}
func adrCaller(pr access.Principal) context.Context {
	return access.WithCaller(context.Background(), access.Caller{Principal: pr, Verified: true})
}

// adrRequest drives a handler directly, wiring the path values and actor the mux
// would otherwise set, and returns the recorder.
func adrRequest(h func(http.ResponseWriter, *http.Request), method, target, body string, pv map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r = r.WithContext(adrCaller(access.Principal{Kind: "user", Name: "bls", Via: "session"}))
	for k, v := range pv {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}
func decodeADR(t *testing.T, w *httptest.ResponseRecorder) adrs.Record {
	t.Helper()
	var d adrs.Record
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode ADR: %v (%s)", err, w.Body.String())
	}
	return d
}
func TestADRCreateListSupersede(t *testing.T) {
	t.Parallel()
	db, service, projects, _ := adrHTTPFixture(t)
	_ = db

	// Create: a fresh ADR is number 1, status draft.
	w := adrRequest(NewADRs(service, projects).CreateADR, "POST", "/api/v1/projects/proj/adrs",
		`{"title":"Use SQLite","bodyMd":"one file","tags":" data , storage ,, "}`,
		map[string]string{"slug": "proj"})
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	first := decodeADR(t, w)
	if first.Number != 1 || first.Status != adrs.Draft {
		t.Fatalf("first ADR: %+v", first)
	}
	// Tags are normalized: trimmed, empties dropped.
	if first.Tags != "data,storage" {
		t.Fatalf("tags not normalized: %q", first.Tags)
	}

	// Accept it (draft→accepted is allowed).
	w = adrRequest(NewADRs(service, projects).PatchADR, "PATCH", "/api/v1/adrs/"+first.ID.String(), `{"status":"accepted"}`,
		map[string]string{"id": first.ID.String()})
	if w.Code != 200 {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	if got := decodeADR(t, w); got.Status != adrs.Accepted {
		t.Fatalf("status after accept: %q", got.Status)
	}

	// An accepted ADR is immutable: a content edit is refused.
	w = adrRequest(NewADRs(service, projects).PatchADR, "PATCH", "/api/v1/adrs/"+first.ID.String(), `{"bodyMd":"rewritten"}`,
		map[string]string{"id": first.ID.String()})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "immutable") {
		t.Fatalf("editing an accepted ADR must 409 immutable: %d %s", w.Code, w.Body.String())
	}

	// Supersede it: a new draft ADR (number 2) that points back at #1.
	w = adrRequest(NewADRs(service, projects).SupersedeADR, "POST", "/api/v1/adrs/"+first.ID.String()+"/supersede",
		`{"title":"Use Postgres"}`, map[string]string{"id": first.ID.String()})
	if w.Code != 201 {
		t.Fatalf("supersede: %d %s", w.Code, w.Body.String())
	}
	second := decodeADR(t, w)
	if second.Number != 2 || second.SupersedesID != first.ID || second.Status != adrs.Draft {
		t.Fatalf("superseding ADR wrong: %+v", second)
	}
	// The default body carries the provenance line.
	if !strings.Contains(second.BodyMD, "Supersedes ADR-001") {
		t.Fatalf("superseding body should cite the predecessor: %q", second.BodyMD)
	}

	// Accepting the successor retires the predecessor to 'superseded'.
	w = adrRequest(NewADRs(service, projects).PatchADR, "PATCH", "/api/v1/adrs/"+second.ID.String(), `{"status":"accepted"}`,
		map[string]string{"id": second.ID.String()})
	if w.Code != 200 {
		t.Fatalf("accept successor: %d %s", w.Code, w.Body.String())
	}
	detail, err := service.Get(adrCaller(access.Principal{Kind: "user", Name: "bls", Via: "open"}), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := detail.ADR
	if old.Status != adrs.Superseded {
		t.Fatalf("predecessor must be superseded, got %q", old.Status)
	}
	// History is kept: the old record's body is untouched.
	if old.BodyMD != "one file" {
		t.Fatalf("predecessor body was mutated: %q", old.BodyMD)
	}

	// handleGetADR reports who superseded it.
	w = adrRequest(NewADRs(service, projects).GetADR, "GET", "/api/v1/adrs/"+first.ID.String(), "", map[string]string{"id": first.ID.String()})
	if w.Code != 200 {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		ADR            adrs.Record `json:"adr"`
		SupersededByID ids.ID      `json:"supersededById"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SupersededByID != second.ID {
		t.Fatalf("supersededById = %d, want %d", got.SupersededByID, second.ID)
	}

	// The project list keeps both, newest number first.
	w = adrRequest(NewADRs(service, projects).ListProjectADRs, "GET", "/api/v1/projects/proj/adrs", "",
		map[string]string{"slug": "proj"})
	var list []adrs.Record
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Number != 2 || list[1].Number != 1 {
		t.Fatalf("list order/size wrong: %+v", list)
	}
}

func TestADRTransitionRules(t *testing.T) {
	t.Parallel()
	db, service, projects, _ := adrHTTPFixture(t)
	_ = db
	// A draft cannot jump straight to superseded.
	draft := decodeADR(t, adrRequest(NewADRs(service, projects).CreateADR, "POST", "/api/v1/projects/proj/adrs",
		`{"title":"T"}`, map[string]string{"slug": "proj"}))
	w := adrRequest(NewADRs(service, projects).PatchADR, "PATCH", "/api/v1/adrs/"+draft.ID.String(), `{"status":"superseded"}`,
		map[string]string{"id": draft.ID.String()})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "cannot transition") {
		t.Fatalf("illegal transition must 422: %d %s", w.Code, w.Body.String())
	}
	// Only accepted ADRs can be superseded — a draft cannot.
	w = adrRequest(NewADRs(service, projects).SupersedeADR, "POST", "/api/v1/adrs/"+draft.ID.String()+"/supersede", `{}`,
		map[string]string{"id": draft.ID.String()})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "only accepted") {
		t.Fatalf("superseding a draft must 422: %d %s", w.Code, w.Body.String())
	}
	// A soft (draft) ADR CAN have its content edited.
	w = adrRequest(NewADRs(service, projects).PatchADR, "PATCH", "/api/v1/adrs/"+draft.ID.String(), `{"title":"T2","bodyMd":"more","expected":{"title":"T","bodyMd":""}}`,
		map[string]string{"id": draft.ID.String()})
	if w.Code != 200 {
		t.Fatalf("draft edit must succeed: %d %s", w.Code, w.Body.String())
	}
	if got := decodeADR(t, w); got.Title != "T2" || got.BodyMD != "more" {
		t.Fatalf("draft edit not applied: %+v", got)
	}
	// An edit that started from text replaced since is refused with a code
	// the editor recognises, so it can offer the current text.
	w = adrRequest(NewADRs(service, projects).PatchADR, "PATCH", "/api/v1/adrs/"+draft.ID.String(), `{"bodyMd":"mine","expected":{"bodyMd":""}}`,
		map[string]string{"id": draft.ID.String()})
	if w.Code != 409 || w.Header().Get("X-Forge-Deny-Code") != "adr-text-moved" {
		t.Fatalf("an edit from moved text must 409 adr-text-moved: %d %s %q", w.Code, w.Body.String(), w.Header().Get("X-Forge-Deny-Code"))
	}
}

func TestADRCreateRequiresTitle(t *testing.T) {
	t.Parallel()
	db, service, projects, _ := adrHTTPFixture(t)
	_ = db
	w := adrRequest(NewADRs(service, projects).CreateADR, "POST", "/api/v1/projects/proj/adrs", `{"title":"   "}`,
		map[string]string{"slug": "proj"})
	if w.Code != 400 {
		t.Fatalf("blank title must 400: %d %s", w.Code, w.Body.String())
	}
}

func TestADRLibraryFilters(t *testing.T) {
	t.Parallel()
	db, service, projects, pid := adrHTTPFixture(t)
	_ = db
	// Two accepted, one draft; distinct tags and bodies.
	mk := func(title, body, tags string, decision adrs.Decision) {
		adr, err := service.Create(adrCaller(access.Principal{Kind: "user", Name: "bls", Via: "open"}), adrs.Create{ProjectID: pid, Title: title, BodyMD: body, Tags: tags})
		if err != nil {
			t.Fatal(err)
		}
		if decision != 0 {
			if _, err := service.Edit(adrCaller(access.Principal{Kind: "user", Name: "bls", Via: "open"}), adrs.Edit{ID: adr.ID, Decision: decision}); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("Adopt SQLite", "single file database", "data", adrs.Accept)
	mk("Adopt Traefik", "edge router", "infra", adrs.Accept)
	mk("Maybe GraphQL", "still thinking", "api", 0)

	call := func(query string) []adrs.Record {
		w := adrRequest(NewADRs(service, projects).ADRLibrary, "GET", "/api/v1/adrs"+query, "", nil)
		if w.Code != 200 {
			t.Fatalf("library %s: %d %s", query, w.Code, w.Body.String())
		}
		var out []adrs.Record
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := call(""); len(got) != 3 {
		t.Fatalf("unfiltered library size: %d", len(got))
	}
	if got := call("?status=accepted"); len(got) != 2 {
		t.Fatalf("status filter: %d", len(got))
	}
	if got := call("?tag=infra"); len(got) != 1 || got[0].Title != "Adopt Traefik" {
		t.Fatalf("tag filter: %+v", got)
	}
	if got := call("?q=SQLite"); len(got) != 1 || got[0].Title != "Adopt SQLite" {
		t.Fatalf("q on title: %+v", got)
	}
	if got := call("?q=router"); len(got) != 1 || got[0].Title != "Adopt Traefik" {
		t.Fatalf("q on body: %+v", got)
	}
	if got := call("?project=proj"); len(got) != 3 {
		t.Fatalf("project filter: %d", len(got))
	}
	if got := call("?project=ghost"); len(got) != 0 {
		t.Fatalf("unknown project should be empty: %+v", got)
	}
}

func TestADRHTTPReturnsLifecycleOptionsAndAcceptsEmptySuccessorBody(t *testing.T) {
	t.Parallel()
	db, service, projects, _ := adrHTTPFixture(t)
	_ = db
	out := adrRequest(NewADRs(service, projects).CreateADR, "POST", "/api/v1/projects/proj/adrs", `{"title":"Decision"}`, map[string]string{"slug": "proj"})
	if out.Code != 201 {
		t.Fatal(out.Body.String())
	}
	v := decodeADR(t, out)
	out = adrRequest(NewADRs(service, projects).PatchADR, "PATCH", "/api/v1/adrs/"+v.ID.String(), `{"status":"accepted"}`, map[string]string{"id": v.ID.String()})
	if out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	out = adrRequest(NewADRs(service, projects).GetADR, "GET", "/api/v1/adrs/"+v.ID.String(), "", map[string]string{"id": v.ID.String()})
	var detail adrs.Detail
	if err := json.Unmarshal(out.Body.Bytes(), &detail); err != nil || out.Code != 200 {
		t.Fatalf("detail: %s %v", out.Body.String(), err)
	}
	if detail.Options.ContentEditable || !detail.Options.CanSupersede || len(detail.Options.Transitions) != 1 || detail.Options.Transitions[0] != adrs.Deprecated {
		t.Fatalf("lifecycle options: %+v", detail.Options)
	}
	out = adrRequest(NewADRs(service, projects).SupersedeADR, "POST", "/api/v1/adrs/"+v.ID.String()+"/supersede", "", map[string]string{"id": v.ID.String()})
	if out.Code != 201 {
		t.Fatalf("empty optional body: %d %s", out.Code, out.Body.String())
	}
	if next := decodeADR(t, out); next.SupersedesID != v.ID || next.Status != adrs.Draft {
		t.Fatalf("successor: %+v", next)
	}
	for _, path := range []string{"get", "create", "edit"} {
		req := httptest.NewRequest("GET", "/api/v1/adrs/"+v.ID.String(), nil)
		req = req.WithContext(adrCaller(access.Principal{Kind: "user", Name: "bls", Via: "session"}))
		req.SetPathValue("id", v.ID.String())
		out = httptest.NewRecorder()
		switch path {
		case "get":
			t.Cleanup(db.FailNextStatement("SELECT project_id FROM adrs"))
			NewADRs(service, projects).GetADR(out, req)
		case "create":
			req = httptest.NewRequest("POST", "/api/v1/projects/proj/adrs", strings.NewReader(`{"title":"unrecorded"}`))
			req.SetPathValue("slug", "proj")
			req = req.WithContext(adrCaller(access.Principal{Kind: "user", Name: "bls", Via: "session"}))
			t.Cleanup(db.FailNextStatement("INSERT INTO audit_log"))
			NewADRs(service, projects).CreateADR(out, req)
		case "edit":
			req = httptest.NewRequest("PATCH", "/api/v1/adrs/"+v.ID.String(), strings.NewReader(`{"status":"deprecated"}`))
			req.SetPathValue("id", v.ID.String())
			req = req.WithContext(adrCaller(access.Principal{Kind: "user", Name: "bls", Via: "session"}))
			t.Cleanup(db.FailNextStatement("INSERT INTO audit_log"))
			NewADRs(service, projects).PatchADR(out, req)
		}
		if out.Code != 500 {
			t.Fatalf("%s storage failure became %d: %s", path, out.Code, out.Body.String())
		}
	}
}

func TestADRPolicyReadFailureCannotAuthorizeMutation(t *testing.T) {
	t.Parallel()
	db, service, projects, pid := adrHTTPFixture(t)
	t.Cleanup(db.FailNextStatement("-- name: ActiveGrants"))
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"title":"unrecorded"}`)).WithContext(adrCaller(access.Principal{Kind: "user", Name: "bls", Via: "session"}))
	r.SetPathValue("slug", "proj")
	w := httptest.NewRecorder()
	NewADRs(service, projects).CreateADR(w, r)
	if w.Code != 500 {
		t.Fatalf("policy failure became %d: %s", w.Code, w.Body)
	}
	recorded, err := service.List(adrCaller(access.Principal{Kind: "user", Name: "bls", Via: "session"}), adrs.Filter{ProjectID: pid})
	if err != nil || len(recorded) != 0 {
		t.Fatalf("policy failure recorded an ADR: %+v %v", recorded, err)
	}
	if receipts := receiptsOfKind(t, db, "adr.created"); len(receipts) != 0 {
		t.Fatalf("policy failure left a receipt: %+v", receipts)
	}
}

package apptown

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/httpapi"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/projectlifecycle"
	"github.com/flokkit/apptown/internal/ids"
)

// Project lifecycle over HTTP: slug law, name defaulting, conflict, patch,
// and the audit trail behind each act.
func TestProjectLifecycleHTTP(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{DockerClient: newFakeDocker()})
	testAdministrator(t, a, "bls")

	h := a.Handler()

	if w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "Bad Slug"}); w.Code != 400 {
		t.Fatalf("bad slug accepted: %d", w.Code)
	}
	w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "egg-catch"})
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"name":"egg-catch"`) {
		t.Fatalf("create (name defaults to slug): %d %s", w.Code, w.Body.String())
	}
	if w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "egg-catch"}); w.Code != 409 {
		t.Fatalf("duplicate slug must 409: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/api/v1/projects/egg-catch", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("get: %d", rec.Code)
	}
	if audits := auditCount(t, a.DB, "project.created"); audits != 1 {
		t.Fatalf("project creation must be audited: %d", audits)
	}
	if st := deleteProjectAs(t, a, "bls", "egg-catch"); st.State != project.Deleted {
		t.Fatalf("delete: %+v", st)
	}
	if op := purgeProjectAs(t, a, "bls", "egg-catch"); op.State != operations.Succeeded {
		t.Fatalf("purge: %s %q", op.State, op.Detail)
	}
	if _, err := a.Projects.BySlug(context.Background(), "egg-catch"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted project is still found: %v", err)
	}
	deleted := 0
	for _, entry := range auditEntries(t, a.DB, "project.deleted") {
		if entry.Subject == "egg-catch" {
			deleted++
		}
	}
	if deleted != 1 {
		t.Fatalf("project deletion receipts: %d", deleted)
	}
}

// The browser's frozen generated request crosses the real admission boundary:
// automatic collision selection happens once, lost-response replay returns the
// original result, and its permanent alias resolves current identity after rename.
func TestDescriptionFirstCreationRequestReplaysThroughHTTPAndCanonicalAlias(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{DockerClient: newFakeDocker()})
	testAdministrator(t, a, "bls")
	h := a.Handler()
	if w := postJSON(h, "/api/v1/projects", map[string]string{"slug": "customer-ledger"}); w.Code != 201 {
		t.Fatalf("collision fixture: %d %s", w.Code, w.Body.String())
	}
	input := httpapi.ProjectDraft{RequestID: ids.New(time.Now()), AutomaticAddress: true, Slug: "customer-ledger", Name: "Customer ledger", Blurb: "Track customer requests", Setup: &projectlifecycle.CreationSetup{Origin: "empty", Services: []projectlifecycle.CreationService{}, Upstream: projectlifecycle.CreationUpstream{AttachmentRequestID: ids.New(time.Now())}}}
	create := func(in httpapi.ProjectDraft) project.Project {
		t.Helper()
		w := postJSON(h, "/api/v1/projects", in)
		var p project.Project
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 201 {
			t.Fatalf("creation: %d %s %v", w.Code, w.Body.String(), err)
		}
		return p
	}
	first := create(input)
	if first.ID.IsZero() || first.Slug == input.Slug || first.Name != input.Name || first.Blurb != input.Blurb {
		t.Fatalf("automatic selected result: %+v", first)
	}
	repository, err := first.Repository(a.cfg.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repository, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local request unexpectedly initialized repository: %v", err)
	}
	renamed := "customer-ledger-renamed"
	patch, _ := json.Marshal(project.SettingsPatch{RequestID: ids.New(time.Now()), ExpectedRevision: &first.SettingsRevision, Slug: &renamed})
	if w := authzCall(h, "bls", "PATCH", "/api/v1/projects/"+first.Slug, string(patch)); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	replay := create(input)
	if replay.ID != first.ID || replay.Slug != first.Slug || replay.SettingsRevision != first.SettingsRevision {
		t.Fatalf("replay did not return original result: %+v / %+v", first, replay)
	}
	w := authzCall(h, "bls", "GET", "/api/v1/projects/"+replay.Slug, "")
	var canonical httpapi.ProjectDetail
	if err := json.Unmarshal(w.Body.Bytes(), &canonical); err != nil || w.Code != 200 || canonical.ID != first.ID || canonical.Slug != renamed {
		t.Fatalf("canonical alias read: %d %s %v", w.Code, w.Body.String(), err)
	}
	changed := input
	changed.Blurb = "A different software request"
	if w := postJSON(h, "/api/v1/projects", changed); w.Code != 409 {
		t.Fatalf("same request changed intent: %d %s", w.Code, w.Body.String())
	}
	if count := auditCount(t, a.DB, "project.created"); count != 2 {
		t.Fatalf("replay created another project: %d receipts", count)
	}
	if _, err := os.Stat(filepath.Join(repository, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replay dispatched repository setup: %v", err)
	}
}

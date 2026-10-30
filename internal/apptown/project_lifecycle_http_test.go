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
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
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
// automatic collision selection happens once, and lost-response replay returns
// the original result, whose address names nothing once the project has moved.
func TestDescriptionFirstCreationRequestReplaysThroughHTTP(t *testing.T) {
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
	if first.ID.IsZero() || first.Slug == input.Slug || first.Name != input.Name || first.Blurb != "" || first.BlurbSource != "generated" {
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
	patch, _ := json.Marshal(project.SettingsPatch{ExpectedRevision: &first.SettingsRevision, Slug: &renamed})
	if w := authzCall(h, "bls", "PATCH", "/api/v1/projects/"+first.Slug, string(patch)); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	replay := create(input)
	if replay.ID != first.ID || replay.Slug != first.Slug || replay.SettingsRevision != first.SettingsRevision {
		t.Fatalf("replay did not return original result: %+v / %+v", first, replay)
	}
	// The replayed result names the address the project had then, which
	// names nothing now.
	if w := authzCall(h, "bls", "GET", "/api/v1/projects/"+replay.Slug, ""); w.Code != 404 {
		t.Fatalf("old address read: %d %s", w.Code, w.Body.String())
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

// Public HTTP creation, job execution and authorized detail reading preserve
// the full request with both an empty and a generated display description.
func TestProjectDescriptionHTTPHistoryAndModelFailure(t *testing.T) {
	t.Parallel()
	a := testApp(t, Config{DockerClient: newFakeDocker()})
	testAdministrator(t, a, "bls")
	failing := true
	setFastSlot(t, a, fakeModelProvider(t, a.Integrations.Bindings, func(q providerQuestion) (int, string) {
		if failing {
			return 503, `{"error":{"message":"unavailable"}}`
		}
		return 200, chatAnswer("Truck assignments and servicing", 12, 4, 0.001)
	}))
	h := a.Handler()
	request := strings.Repeat("Track trucks, their assigned drivers, servicing and inspections. ", 5)
	input := httpapi.ProjectDraft{RequestID: idstest.N(700), Slug: "trucks", Name: "Trucks", Blurb: request}
	w := postJSON(h, "/api/v1/projects", input)
	var p project.Project
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 201 || p.Blurb != "" {
		t.Fatalf("creation: %d %s %v", w.Code, w.Body.String(), err)
	}
	queued := operationJobs(t, a, project.DescriptionJob)
	if len(queued) != 1 {
		t.Fatalf("creation jobs: %+v", queued)
	}
	first := runQueuedJobForTest(t, a, project.DescriptionJob, queued[0].Key)
	if first.State != "completed" || len(first.Errors) != 0 {
		t.Fatalf("model unavailability should finish quietly: %+v", first)
	}
	read := func() httpapi.ProjectDetail {
		t.Helper()
		w := authzCall(h, "bls", "GET", "/api/v1/projects/trucks", "")
		var detail httpapi.ProjectDetail
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 {
			t.Fatalf("detail: %d %s %v", w.Code, w.Body.String(), err)
		}
		if detail.OriginalRequest != request {
			t.Fatalf("lost request: %+v", detail)
		}
		return detail
	}
	if d := read(); d.Blurb != "" || d.BlurbSource != "generated" {
		t.Fatalf("model failure: %+v", d)
	}
	failing = false
	d := projectlifecycle.Descriptions{Service: a.lifecycle, Models: a.modelSelection, Completion: a.inference, Changes: a.Work}
	if err := d.Refresh(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Blurb != "Truck assignments and servicing" || got.BlurbSource != "generated" {
		t.Fatalf("generated detail: %+v", got)
	}
	w = postJSON(h, "/api/v1/projects", input)
	var replay project.Project
	if err := json.Unmarshal(w.Body.Bytes(), &replay); err != nil || w.Code != 201 || replay != p {
		t.Fatalf("replay result: %+v %v", replay, err)
	}
	report, err := a.Usage.Ledger.Summary(t.Context(), 1, p.ID, "")
	if err != nil || report.Totals.Input != 12 || report.Totals.Output != 4 {
		t.Fatalf("gateway usage after failure and success: %+v %v", report.Totals, err)
	}

	// sql-guarantee: even the failed model call keeps its zero-spend gateway line.
	completionCount(t, a.DB, 2, `SELECT count(*) FROM token_usage WHERE project_id=$1 AND kind=$2`, p.ID, project.DescriptionJob)

}

func TestLandedChangeQueuesDescriptionOutsideSettlement(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"generated", "person", "model failure", "inspection"} {
		t.Run(mode, func(t *testing.T) {
			a, source := mergeSettlementFixture(t)
			p, err := a.Projects.ByID(t.Context(), source.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Projects.PublishDescription(t.Context(), p.ID, "Before landing"); err != nil {
				t.Fatal(err)
			}
			if mode == "person" {
				p, _ = a.Projects.ByID(t.Context(), p.ID)
				text := "Our description"
				if _, err := a.lifecycle.UpdateSettings(t.Context(), p.ID, project.SettingsPatch{ExpectedRevision: &p.SettingsRevision, Blurb: &text}, "fixture"); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			setFastSlot(t, a, fakeModelProvider(t, a.Integrations.Bindings, func(q providerQuestion) (int, string) {
				calls++
				if !strings.Contains(q.User, "work to land") {
					t.Errorf("missing landed title: %s", q.User)
				}
				if mode == "model failure" {
					return 503, `{"error":{"message":"offline"}}`
				}
				return 200, chatAnswer("After landing", 15, 2, 0)
			}))
			if mode == "inspection" {
				pendingMergeForTest(t, a, source.RunID, "base")
				if _, err := scmFor(a).Inspect(workflow.SCMInspectionContext(context.Background(), a.DB), source.RunID, provenMergeDriver{result: "observed"}); err != nil {
					t.Fatal(err)
				}
			} else {
				result, err := mergeEffectForTest(t, a, source)
				if err != nil || !result.Merged {
					t.Fatalf("landing: %+v %v", result, err)
				}
			}
			if calls != 0 {
				t.Fatal("landing waited for model")
			}
			queued := operationJobs(t, a, project.DescriptionJob)
			if len(queued) != 1 || queued[0].Key != source.ProjectID.String()+"/landed/"+source.ChangeID.String() {
				t.Fatalf("landing jobs: %+v", queued)
			}
			runQueuedJobForTest(t, a, project.DescriptionJob, queued[0].Key)
			held, _ := a.Projects.ByID(t.Context(), p.ID)
			want := "After landing"
			if mode == "person" {
				want = "Our description"
			}
			if mode == "model failure" {
				want = "Before landing"
			}
			if held.Blurb != want {
				t.Fatalf("description: %q want %q", held.Blurb, want)
			}
			if mode == "person" && calls != 0 {
				t.Fatal("person description called model")
			}
		})
	}
}

func TestDescriptionQueueFailureRollsBackLandingSettlement(t *testing.T) {
	t.Parallel()
	a, source := mergeSettlementFixture(t)
	// sql-guarantee: force transactional River admission to fail at the real
	// SQL boundary (River uses the native transaction, not the query wrapper).
	completionExec(t, a.DB, `ALTER TABLE river_job ADD CONSTRAINT refuse_description CHECK (args->>'operation'<>'project-description')`)
	result, err := mergeEffectForTest(t, a, source)
	completionExec(t, a.DB, `ALTER TABLE river_job DROP CONSTRAINT refuse_description`)
	if err == nil || result.Merged {
		t.Fatalf("unqueued landing settlement: %+v %v", result, err)
	}
	if got := changeRecord(t, a, source.ChangeID).LandedSHA; got != "" {
		t.Fatalf("failed settlement recorded %q", got)
	}
}

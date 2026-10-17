package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/progress"
)

func workflowLiveSpec() schema.Spec {
	return schema.Spec{Kind: "pipeline", Role: "working", Stages: []schema.Stage{{Name: "work", Steps: []schema.Step{{Name: "agent", Uses: "agent/pi"}}}}}
}

func workflowLiveStep(t *testing.T, f workflowHTTPFixture) ids.ID {
	t.Helper()
	snapshot, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 || snapshot.Steps[0].Type != "agent/pi" {
		t.Fatalf("agent step fixture: %+v %v", snapshot, err)
	}
	return snapshot.Steps[0].ID
}

func TestStepLiveReturnsOffsetsAndPersistsFinalTail(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixtureWithSpec(t, true, workflowLiveSpec(), nil, nil)
	stepID := workflowLiveStep(t, f)
	var feeds progress.Feeds
	read := func(from int) (running bool, lines []string, total int) {
		t.Helper()
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/steps/%d/live?from=%d", stepID, from), nil).WithContext(f.Context)
		r.SetPathValue("id", fmt.Sprint(stepID))
		w := httptest.NewRecorder()
		NewRunProgress(f.Work, &feeds).StepLive(w, r)
		var response struct {
			Running bool     `json:"running"`
			Lines   []string `json:"lines"`
			Total   int      `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 {
			t.Fatalf("live response: %d %s (%v)", w.Code, w.Body.String(), err)
		}
		return response.Running, response.Lines, response.Total
	}
	feeds.Start(stepID)
	feeds.Append(stepID, "first", "second")
	if running, lines, total := read(1); !running || len(lines) != 1 || lines[0] != "second" || total != 2 {
		t.Fatalf("live offset: running=%v lines=%v total=%d", running, lines, total)
	}
	workflow.CaptureProgress(context.Background(), f.DB, &feeds, stepID, nil)
	if running, lines, total := read(0); running || len(lines) != 2 || lines[0] != "first" || total != 2 {
		t.Fatalf("persisted tail: running=%v lines=%v total=%d", running, lines, total)
	}
}

func TestStepLiveChecksAuthorityForMemoryAndPersistedEvidence(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPAuthorityFixtureWithSpec(t, workflowLiveSpec())
	stepID := workflowLiveStep(t, f)
	var feeds progress.Feeds
	feeds.Start(stepID)
	feeds.Append(stepID, "private live words")
	read := func(who, via string, verified bool) *httptest.ResponseRecorder {
		ctx := context.Background()
		if verified {
			ctx = access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "user", Name: who, Via: via}, Verified: true})
		}
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/steps/%d/live", stepID), nil).WithContext(ctx)
		r.SetPathValue("id", fmt.Sprint(stepID))
		r.Header.Set("X-Forge-Actor", "owner")
		w := httptest.NewRecorder()
		NewRunProgress(f.Work, &feeds).StepLive(w, r)
		return w
	}
	for _, persisted := range []bool{false, true} {
		if persisted {
			workflow.CaptureProgress(context.Background(), f.DB, &feeds, stepID, nil)
		}
		for _, tc := range []struct {
			who, via string
			verified bool
			code     int
		}{
			{"", "", false, 401},
			{"outsider", "token", true, 403},
			{"owner", "oauth", true, 403},
		} {
			got := read(tc.who, tc.via, tc.verified)
			if got.Code != tc.code || strings.Contains(got.Body.String(), "private live words") {
				t.Fatalf("persisted=%v identity=%+v: %d %s", persisted, tc, got.Code, got.Body.String())
			}
		}
		got := read("owner", "token", true)
		if got.Code != 200 || !strings.Contains(got.Body.String(), "private live words") {
			t.Fatalf("owner persisted=%v: %d %s", persisted, got.Code, got.Body.String())
		}
	}
	grants, err := f.Access.GrantsAt(context.Background(), access.ScopeProject, fmt.Sprint(f.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	revoked := 0
	for _, grant := range grants {
		if grant.SubjectType == "user" && grant.Subject == "owner" {
			if _, err := f.Access.Revoke(context.Background(), grant.ID, "bls"); err != nil {
				t.Fatal(err)
			}
			revoked++
		}
	}
	if revoked != 2 {
		t.Fatalf("expected two owner grants revoked, got %d", revoked)
	}
	if got := read("owner", "token", true); got.Code != 403 {
		t.Fatalf("revoked reader: %d %s", got.Code, got.Body.String())
	}
}

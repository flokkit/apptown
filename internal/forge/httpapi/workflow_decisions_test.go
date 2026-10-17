package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

func stepDecisionRequest(handler http.HandlerFunc, ctx context.Context, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(ctx)
	r.SetPathValue("id", id)
	r.Header.Set("X-Forge-Actor", "forged-reviewer")
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestStepDecisionHTTPRejectsInvalidRequestsWithoutRecordingDecisions(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPAuthorityFixture(t)
	f.Work.Wait()
	before, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil || len(before.Steps) != 1 {
		t.Fatalf("approval fixture: %+v %v", before, err)
	}
	step := before.Steps[0]
	id := fmt.Sprint(step.ID)
	h := NewRuns(f.Work)
	outsider := access.WithCaller(context.Background(), access.Caller{Principal: access.Principal{Kind: "user", Name: "outsider", Via: "session"}, Verified: true})
	for _, tc := range []struct {
		name, id, body string
		handler        http.HandlerFunc
		ctx            context.Context
		status         int
	}{
		{"approval ID", "invalid", `{"decision":"approve"}`, h.Approve, f.Context, 400},
		{"approval JSON", id, `{"decision":`, h.Approve, f.Context, 400},
		{"approval choice", id, `{"decision":"abstain"}`, h.Approve, f.Context, 400},
		{"approval missing step", "0000000f-423f-7000-8000-000000000000", `{"decision":"approve"}`, h.Approve, f.Context, 404},
		{"approval no caller", id, `{"decision":"approve"}`, h.Approve, context.Background(), 401},
		{"approval foreign scope", id, `{"decision":"approve"}`, h.Approve, outsider, 403},
		{"override ID", "invalid", `{"reason":"reviewed"}`, h.Override, f.Context, 400},
		{"override JSON", id, `{"reason":`, h.Override, f.Context, 400},
		{"override reason type", id, `{"reason":4}`, h.Override, f.Context, 400},
		{"override missing step", "0000000f-423f-7000-8000-000000000000", `{"reason":"reviewed"}`, h.Override, f.Context, 404},
		{"override no caller", id, `{"reason":"reviewed"}`, h.Override, context.Background(), 401},
		{"override foreign scope", id, `{"reason":"reviewed"}`, h.Override, outsider, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := stepDecisionRequest(tc.handler, tc.ctx, tc.id, tc.body)
			var response struct{ Error string }
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != tc.status || response.Error == "" {
				t.Fatalf("decision response: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			after, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
			if err != nil || len(after.Steps) != 1 {
				t.Fatalf("read decision: %+v %v", after, err)
			}
			got := after.Steps[0]
			if got.ID != step.ID || got.Status != step.Status || !bytes.Equal(got.Evidence, step.Evidence) || got.OverrideBy != step.OverrideBy {
				t.Fatalf("rejected request changed the decision: %+v", got)
			}
		})
	}
	// A valid request against the same fixture must still be able to decide it.
	w := stepDecisionRequest(h.Approve, f.Context, id, `{"decision":"approve"}`)
	var out workflow.Step
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.ID != step.ID || out.Status != workflow.StatusPass {
		t.Fatalf("valid approval: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	f.Work.Wait()
}

func TestApprovalHTTPRejectionPreservesFeedbackAndCannotBeReplaced(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	f.Work.Wait()
	snapshot, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("approval fixture: %+v %v", snapshot, err)
	}
	stepID := snapshot.Steps[0].ID
	h := NewRuns(f.Work)
	w := stepDecisionRequest(h.Approve, f.Context, fmt.Sprint(stepID), `{"user":"forged-reviewer","decision":"reject","comment":"Keep the old controls.\nFix the keyboard input."}`)
	var out struct {
		ID       ids.ID `json:"id"`
		RunID    ids.ID `json:"runId"`
		Status   string `json:"status"`
		Evidence struct {
			Approver, Decision, Comment string
			Decisions                   []struct{ Approver, Decision, Comment string }
		} `json:"evidence"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("reject response: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	const feedback = "Keep the old controls.\nFix the keyboard input."
	if out.ID != stepID || out.RunID != f.RunID || out.Status != "fail" || out.Evidence.Approver != "bls" || out.Evidence.Decision != "reject" || out.Evidence.Comment != feedback || len(out.Evidence.Decisions) != 1 {
		t.Fatalf("rejection lost identity, decision or feedback: %+v", out)
	}
	f.Work.Wait()
	w = stepDecisionRequest(h.Approve, f.Context, fmt.Sprint(stepID), `{"decision":"approve","comment":"replace my vote"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "already") {
		t.Fatalf("closed decision should explain its conflict: %d %s", w.Code, w.Body.String())
	}
	saved, err := f.Work.Snapshot(f.Context, f.RunID, workflow.DetailView)
	if err != nil || len(saved.Steps) != 1 {
		t.Fatalf("read rejected decision: %+v %v", saved, err)
	}
	evidence, err := workflow.ReadApprovalEvidence(saved.Steps[0].Evidence)
	if err != nil || saved.Steps[0].Status != workflow.StatusFail || len(evidence.Decisions) != 1 || evidence.Decisions[0].Decision != workflow.VoteReject || evidence.Decisions[0].Approver != "bls" || evidence.Decisions[0].Comment != feedback {
		t.Fatalf("conflict replaced recorded rejection: %+v %v", evidence, err)
	}
}

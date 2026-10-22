package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/fault"
)

type changeVerifierFunc func(context.Context, *workflow.Change, verification.Selection) (map[string]any, error)

func (f changeVerifierFunc) Check(ctx context.Context, ch *workflow.Change, sel verification.Selection) (map[string]any, error) {
	return f(ctx, ch, sel)
}

// The transport enforces run-token scope before calling the verifier,
// passes the commit the caller asks about to it unchanged (a named commit,
// the published tip, or neither for an empty body), and distinguishes
// repository absence, a commit the owner refuses and an execution failure.
func TestChangeCheckHTTPRunScopeAndErrors(t *testing.T) {
	t.Parallel()
	f := newWorkflowHTTPFixture(t, true)
	sibling, err := f.Work.Create(f.Context, workflow.ChangeDraft{ProjectID: f.ProjectID, Title: "Sibling"})
	if err != nil {
		t.Fatal(err)
	}
	named := strings.Repeat("ab", 20)
	for _, tc := range []struct {
		name, id, receipt, body string
		sel                     verification.Selection
		failure                 error
		code                    int
		calls                   int
		words                   string
	}{
		{name: "malformed", id: "bad", receipt: "alice", code: 400, words: "bad id"},
		{name: "missing", id: "0000000f-423f-7000-8000-000000000000", receipt: "alice", code: 404, words: "no such change"},
		{name: "own change", id: fmt.Sprint(f.ChangeID), receipt: fmt.Sprintf("run/%d", f.RunID), code: 200, calls: 1, words: `"ok":true`},
		{name: "sibling", id: fmt.Sprint(sibling.ID), receipt: fmt.Sprintf("run/%d", f.RunID), code: 403, words: "only check its own change"},
		{name: "missing run", id: fmt.Sprint(f.ChangeID), receipt: "run/0000000f-423f-7000-8000-000000000000", code: 403, words: "only check its own change"},
		{name: "no repository", id: fmt.Sprint(f.ChangeID), receipt: "alice", failure: fmt.Errorf("change %d has no repo workspace to check: %w", f.ChangeID, scm.ErrNoWorkspace), code: 400, calls: 1, words: "no repo workspace to check"},
		{name: "execution failure", id: fmt.Sprint(f.ChangeID), receipt: "alice", failure: errors.New("check failed"), code: 500, calls: 1, words: "check failed"},
		// The commit the caller asks about reaches the owner as sent; the
		// owner's refusal of it keeps its kind and words.
		{name: "named commit", id: fmt.Sprint(f.ChangeID), receipt: "alice", body: `{"commit":"` + named + `"}`, sel: verification.Selection{Commit: named}, code: 200, calls: 1, words: `"ok":true`},
		{name: "named commit not here", id: fmt.Sprint(f.ChangeID), receipt: "alice", body: `{"commit":"` + named + `"}`, sel: verification.Selection{Commit: named}, failure: fault.Wrap(fault.NotFound, fmt.Errorf("commit %s is not on AppTown yet", named)), code: 404, calls: 1, words: "commit " + named + " is not on AppTown yet"},
		// --published is its own request, distinct from an empty body.
		{name: "published tip", id: fmt.Sprint(f.ChangeID), receipt: "alice", body: `{"published":true}`, sel: verification.Selection{Published: true}, code: 200, calls: 1, words: `"ok":true`},
		{name: "empty body", id: fmt.Sprint(f.ChangeID), receipt: "alice", body: `{}`, code: 200, calls: 1, words: `"ok":true`},
		{name: "malformed body", id: fmt.Sprint(f.ChangeID), receipt: "alice", body: `{"commit":`, code: 400, words: "bad json"},
		{name: "sibling with a commit", id: fmt.Sprint(sibling.ID), receipt: fmt.Sprintf("run/%d", f.RunID), body: `{"commit":"` + named + `"}`, code: 403, words: "only check its own change"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := NewChangeChecks(f.DB, changeVerifierFunc(func(_ context.Context, got *workflow.Change, sel verification.Selection) (map[string]any, error) {
				calls++
				if got.ID != f.ChangeID || sel != tc.sel {
					t.Errorf("checked %d at %+v, want %d at %+v", got.ID, sel, f.ChangeID, tc.sel)
				}
				return map[string]any{"ok": true}, tc.failure
			}))
			req := httptest.NewRequest("POST", "/api/v1/changes/"+tc.id+"/check", strings.NewReader(tc.body))
			req.SetPathValue("id", tc.id)
			req = req.WithContext(access.WithCaller(req.Context(), access.Caller{Receipt: tc.receipt}))
			out := httptest.NewRecorder()
			h.Check(out, req)
			if out.Code != tc.code || calls != tc.calls || !strings.Contains(out.Body.String(), tc.words) {
				t.Fatalf("status=%d calls=%d body=%s", out.Code, calls, out.Body.String())
			}
		})
	}
}

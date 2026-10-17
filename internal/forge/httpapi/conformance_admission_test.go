package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/project"
)

// Record the handoff options and admit a real review. The Fleet owner tests
// cover walking and settling it; HTTP must select the intended project set.
type admittedHTTPReview struct {
	checks  *conformance.Service
	options conformance.ReviewOptions
	calls   int
}

func (f *admittedHTTPReview) StartFleetReviewWith(ctx context.Context, trigger string, opts conformance.ReviewOptions) (*conformance.Review, error) {
	f.calls++
	f.options = opts
	e, err := f.checks.BeginReview(ctx, conformance.ReviewRequest{Trigger: trigger, Projects: opts.Projects})
	if err != nil {
		return nil, err
	}
	r := e.Review()
	return &r, nil
}
func TestConformanceReviewHTTPValidatesBeforeAdmittingAndPreservesScope(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	pid, _ := confirmationHTTPProject(t, db, "reviewed")
	confirmationHTTPProject(t, db, "elsewhere")
	checks := conformance.New(db, confirmationHTTPAuthority{}, confirmationHTTPSource{}, nil)
	fleet := &admittedHTTPReview{checks: checks}
	h := NewConformanceReviews(project.New(db, nil), fleet)
	ctx := confirmationHTTPContext(t, db)
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		h.RunConformanceReview(w, r)
		return w
	}
	for _, tc := range []struct {
		body string
		code int
	}{{`{`, 400}, {`{"projects":false}`, 400}, {`{"projects":["reviewed","missing"]}`, 404},
		// A misspelt key is refused, never read as "the whole estate".
		{`{"project":"reviewed"}`, 400}, {`{"projects":["reviewed"]} {}`, 400}} {
		w := call(tc.body)
		if w.Code != tc.code || fleet.calls != 0 {
			t.Fatalf("refused admission: %d %s calls=%d", w.Code, w.Body.String(), fleet.calls)
		}
	}
	w := call(`{"projects":[" reviewed "],"build":false}`)
	var review conformance.Review
	if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil || w.Code != 202 || review.ID.IsZero() || review.Status != conformance.ReviewRunning || fleet.calls != 1 || fleet.options.Build || len(fleet.options.Projects) != 1 || fleet.options.Projects[0] != pid {
		t.Fatalf("admission scope: %d %s %+v %v", w.Code, w.Body.String(), fleet.options, err)
	}
	w = call("")
	if w.Code != 202 || fleet.calls != 2 || !fleet.options.Build || len(fleet.options.Projects) != 0 {
		t.Fatalf("empty-body estate default: %d %s %+v", w.Code, w.Body.String(), fleet.options)
	}
}

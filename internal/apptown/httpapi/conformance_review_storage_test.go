package httpapi

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/project"
)

func TestReviewHTTPReportsStorageFailureInsteadOfMissingHistory(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	projectID, slug := confirmationHTTPProject(t, db, "reviewed")
	service := conformance.New(db, confirmationHTTPAuthority{}, confirmationHTTPSource{}, nil)
	ctx := confirmationHTTPContext(t, db)
	verdict := confirmationHTTPVerdict(t, service, ctx, projectID, slug, conformance.DoesNotConform)
	handler := NewConformance(service, project.New(db, nil), nil)
	for _, tc := range []struct {
		name     string
		fragment string
		handler  func(*httptest.ResponseRecorder)
	}{
		{name: "detail", fragment: "FROM conformance_verdicts", handler: func(w *httptest.ResponseRecorder) {
			r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
			r.SetPathValue("id", fmt.Sprint(verdict.ReviewID))
			handler.ReviewDetail(w, r)
		}},
		{name: "summaries", fragment: "FROM conformance_reviews", handler: func(w *httptest.ResponseRecorder) {
			handler.ListReviews(w, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
		}},
		{name: "lens", fragment: "FROM conformance_verdicts", handler: func(w *httptest.ResponseRecorder) {
			handler.Conformance(w, httptest.NewRequest("GET", fmt.Sprintf("/?review=%d", verdict.ReviewID), nil).WithContext(ctx))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(dbtest.NextStatementFailure(db, tc.fragment))
			w := httptest.NewRecorder()
			tc.handler(w)
			if w.Code != 500 {
				t.Fatalf("%s: HTTP %d %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

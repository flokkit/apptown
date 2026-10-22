package httpapi

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

func TestProcedureErrorsPreserveAuthorityAndConflictStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		err            error
		fallback, want int
	}{
		{"revoked", fmt.Errorf("settlement: %w", &access.Denied{Reason: "revoked"}), 409, 403},
		{"uplift denied", &access.Denied{}, 422, 403},
		{"unverified", access.ErrPrincipalRequired, 500, 401},
		{"work caller", workflow.ErrCallerRequired, 422, 401},
		{"stale revision", adoption.ErrPreparedRevision, 409, 409},
		{"storage", errors.New("write failed"), 500, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteFaultOr(w, tc.err, tc.fallback)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

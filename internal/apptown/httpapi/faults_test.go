package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

func TestWorkCommandRetryResponses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind workflow.RetryFailure
		code int
	}{
		{workflow.RetryInvalid, 400}, {workflow.RetryUnavailable, 422}, {workflow.RetryConflict, 409},
	} {
		response := httptest.NewRecorder()
		err := fmt.Errorf("operation: %w", &workflow.RetryError{Kind: tc.kind, Reason: "cannot retry this step"})
		WriteFaultOr(response, err, 500)
		if response.Code != tc.code || strings.Contains(response.Body.String(), "operation:") || !strings.Contains(response.Body.String(), "cannot retry this step") {
			t.Fatalf("retry response: %d %s", response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	WriteFaultOr(response, fmt.Errorf("operation: %w", workflow.ErrCallerRequired), 500)
	if response.Code != 401 {
		t.Fatalf("authority refusal became %d", response.Code)
	}
}

// Only a read the owner marked as by identity is "not found". A bare missing
// row (a write that returns its row and recorded nothing) and a failed read
// are the server's failure, never a 404 that tells the caller the thing does
// not exist.
func TestOnlyAMarkedMissingRowIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"marked missing row", database.Missing(fmt.Errorf("reading change 7: %w", sql.ErrNoRows)), http.StatusNotFound},
		{"bare missing row", fmt.Errorf("recording the landing: %w", sql.ErrNoRows), http.StatusInternalServerError},
		{"failed identity read", database.Missing(errors.New("connection reset by peer")), http.StatusInternalServerError},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		WriteFault(w, c.err)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
}

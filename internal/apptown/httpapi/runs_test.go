package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Invalid input is rejected before any Work call; no database or service is needed.
func TestRunInputErrors(t *testing.T) {
	t.Parallel()
	h := NewRuns(nil, nil)
	for _, tc := range []struct {
		name, id, body, message string
		handler                 http.HandlerFunc
	}{
		{"run id", "bad", "", "bad id", h.GetRun},
		{"artifact id", "bad", "", "bad id", h.GetArtifact},
		{"attachment id precedes body", "bad", "{", "bad id", h.AddArtifact},
		{"malformed attachment", "00000000-0001-7000-8000-000000000000", "{", "kind is required", h.AddArtifact},
		{"missing kind", "00000000-0001-7000-8000-000000000000", `{ "body": "hi" }`, "kind is required", h.AddArtifact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			req.SetPathValue("id", tc.id)
			out := httptest.NewRecorder()
			tc.handler(out, req)
			if out.Code != 400 || !strings.Contains(out.Body.String(), tc.message) {
				t.Fatalf("response: %d %s", out.Code, out.Body.String())
			}
		})
	}
}

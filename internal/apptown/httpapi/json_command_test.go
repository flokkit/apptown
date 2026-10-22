package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONCommandInvokesOnlyWithACompleteObject(t *testing.T) {
	t.Parallel()
	type request struct {
		Confirm string `json:"confirm"`
	}
	for _, tc := range []struct {
		name, body string
		policy     jsonBody
		called     bool
		confirm    string
	}{
		{"valid", `{"confirm":"main"}`, jsonBody{}, true, "main"},
		{"unknown field", `{"confirm":"main","future":true}`, jsonBody{}, true, "main"},
		{"valid duplicate", `{"confirm":"old","confirm":"main"}`, jsonBody{}, true, "main"},
		{"partial decode", `{"confirm":"main","confirm":false}`, jsonBody{}, false, ""},
		{"trailing value", `{"confirm":"main"} {}`, jsonBody{}, false, ""},
		{"trailing junk", `{"confirm":"main"} x`, jsonBody{}, false, ""},
		{"required absent", "", jsonBody{}, false, ""},
		{"optional absent", " \n\t", jsonBody{optional: true}, true, ""},
		{"optional malformed", "{", jsonBody{optional: true}, false, ""},
		{"optional null", "null", jsonBody{optional: true}, false, ""},
		{"array", "[]", jsonBody{}, false, ""},
		{"scalar", `"main"`, jsonBody{}, false, ""},
		{"over limit", `{"confirm":"main"}`, jsonBody{limit: 8}, false, ""},
		{"oversized suffix", "{}" + strings.Repeat(" ", 20), jsonBody{limit: 8}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			called := false
			jsonCommand(w, httptest.NewRequest("POST", "/", strings.NewReader(tc.body)), tc.policy, func(in request) {
				called = true
				if in.Confirm != tc.confirm {
					t.Fatalf("decoded %q, want %q", in.Confirm, tc.confirm)
				}
			})
			if called != tc.called {
				t.Fatalf("command invoked=%v, want %v", called, tc.called)
			}
			if !tc.called && w.Code != 400 {
				t.Fatalf("invalid input status: %d", w.Code)
			}
		})
	}
}

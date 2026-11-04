package workflowexec

import "testing"

// Step headlines classify the HTTP status rather than repeating the raw
// provider's claim; no-error zero-output names the selected model.
func TestModelFailureReasons(t *testing.T) {
	t.Parallel()
	for _, row := range []struct{ reported, want string }{
		{"401 raw provider canary", "OpenAI refused the API key (HTTP 401)"},
		{"403 raw provider canary", "OpenAI refused access to gpt-test (HTTP 403)"},
		{"429 raw provider canary", "OpenAI refused the call: rate limit or out of credit (HTTP 429)"},
		{"500 raw provider canary", "OpenAI had an internal error (HTTP 500)"},
		{"503 raw provider canary", "OpenAI had an internal error (HTTP 503)"},
		{"connection refused raw provider canary", "AppTown couldn't reach OpenAI"},
		{"request timed out raw provider canary", "AppTown couldn't reach OpenAI"},
		{"502 AppTown couldn't reach OpenAI", "AppTown couldn't reach OpenAI"},
	} {
		t.Run(row.reported, func(t *testing.T) {
			t.Parallel()
			events := piRefuses(row.reported)
			if got := modelAnswerFailure(0, events, "selected/model"); got != row.want {
				t.Fatalf("%q want %q", got, row.want)
			}
			if got := modelAnswerFailure(1, events, "selected/model"); got != "" {
				t.Fatalf("an earlier answer counts: %s", got)
			}
		})
	}
	if got := modelAnswerFailure(0, nil, "selected/model"); got != "selected/model returned nothing" {
		t.Fatal(got)
	}
	events := []byte(`{"type":"message_end","message":{"role":"assistant","provider":"openai","model":"resolved-model","usage":{"output":0}}}`)
	if got := modelAnswerFailure(0, events, "selected/pattern*"); got != "resolved-model returned nothing" {
		t.Fatal(got)
	}

}

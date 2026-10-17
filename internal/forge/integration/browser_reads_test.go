package integration

import (
	"context"
	"strings"
	"testing"
)

// A browser gets a DSN's public key, never a password typed in with it; a
// server still gets the DSN as stored.
func TestABrowserIsNeverGivenADSNPassword(t *testing.T) {
	t.Parallel()
	s, id, _ := runtimeFixture(t)
	if err := s.Attachments.Attach(t.Context(), id, "errors", "", `{"dsn":"https://public:private-review-canary@errors.example/3"}`, Entered, "review", nil); err != nil {
		t.Fatal(err)
	}
	values, err := s.BrowserEnvironment(context.Background(), id)
	if err != nil || values["SENTRY_DSN"] != "https://public@errors.example/3" || values["GLITCHTIP_DSN"] != values["SENTRY_DSN"] {
		t.Fatalf("browser DSN: %v %v", values, err)
	}
	if env := strings.Join(s.Environment(context.Background(), id), "\n"); !strings.Contains(env, "SENTRY_DSN=https://public:private-review-canary@errors.example/3") {
		t.Fatalf("server env: %s", env)
	}
}

// A browser-values read that fails says so; it is never an empty set.
func TestAnUnreadableFeedbackAttachmentIsAnError(t *testing.T) {
	t.Parallel()
	s, id, _ := runtimeFixture(t)
	if err := s.Attachments.Attach(t.Context(), id, "feedback", "", `{"appKey":42,"url":"https://feedback.example"}`, Entered, "review", nil); err != nil {
		t.Fatal(err)
	}
	values, err := s.BrowserEnvironment(context.Background(), id)
	if err == nil {
		t.Fatalf("malformed attached feedback became successful empty values: %#v", values)
	}
}

// A definition saved before the browser-secret rule is an error naming it,
// not a quietly missing integration.
func TestADefinitionThatNoLongerPassesIsAnError(t *testing.T) {
	t.Parallel()
	s, id, _ := runtimeFixture(t)
	valid := "capability: mailer\ntitle: Mailer\nenv:\n  - {key: URL, value: 'https://mailer.example', browser: true}\n"
	if err := s.ServiceKinds.Save(context.Background(), "mailer", valid, "Mailer", "review"); err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.Attach(t.Context(), id, "mailer", "", `{}`, Entered, "review", nil); err != nil {
		t.Fatal(err)
	}
	legacy := valid + "  - {key: API_KEY, value: '{{secret:resource.key}}', browser: true}\n"
	// sql-guarantee: Store a definition as it could be saved before browser values were checked; ServiceKinds.Save now refuses a value from a secret marked for browsers, so no operation can produce this legacy row.
	if _, err := s.Bindings.db.Exec(`UPDATE service_kinds SET spec_yaml=$1 WHERE capability='mailer'`, legacy); err != nil {
		t.Fatal(err)
	}
	values, err := s.BrowserEnvironment(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "mailer") {
		t.Fatalf("legacy definition silently omitted: %#v %v", values, err)
	}
}

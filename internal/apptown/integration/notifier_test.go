package integration

import (
	"errors"
	"testing"
)

func TestNotifierCatalogAndReferences(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"webhook", "smtp"} {
		spec := FindConnector(CapNotifier, provider)
		if spec == nil || !spec.Multi || spec.Managed {
			t.Fatalf("missing notifier spec %s", provider)
		}
		if (Binding{Capability: CapNotifier, Provider: provider}).HasAutomaticCheck() {
			t.Fatal("notifier must use recorded send test")
		}
		if FeatureArea(CapNotifier, provider) != "setup" {
			t.Fatal("notifier setup area")
		}
	}
	if err := validateNotifierConfig("webhook", `{"urlFrom":["URL"],"digestHour":0}`); err != nil {
		t.Fatal(err)
	}
	// The save endpoint writes every secretRef field, so an SMTP relay
	// without a login arrives with passwordFrom null or empty.
	for _, raw := range []string{
		`{"host":"relay.example","security":"none","from":"notices@example.com","to":"admin@example.com","passwordFrom":null}`,
		`{"host":"relay.example","security":"starttls","from":"notices@example.com","to":"admin@example.com","passwordFrom":[]}`,
		`{"host":"smtp.example","username":"admin","passwordFrom":["PASSWORD"],"from":"notices@example.com","to":"admin@example.com"}`,
	} {
		if err := validateNotifierConfig("smtp", raw); err != nil {
			t.Fatalf("valid SMTP channel rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"host":"smtp.example","username":"admin","passwordFrom":[],"from":"notices@example.com","to":"admin@example.com"}`,
		`{"host":"smtp.example","passwordFrom":["A","B"],"from":"notices@example.com","to":"admin@example.com"}`,
	} {
		if err := validateNotifierConfig("smtp", raw); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid SMTP config accepted: %s", raw)
		}
	}
	for _, raw := range []string{`{"urlFrom":null}`, `{"urlFrom":[]}`, `{"url":"secret"}`, `{"urlFrom":"URL"}`, `{"urlFrom":["URL"],"digestHour":24}`, `{"urlFrom":["URL"],"password":"secret"}`, `{"urlFrom":["URL"],"format":"other"}`} {
		if err := validateNotifierConfig("webhook", raw); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
}

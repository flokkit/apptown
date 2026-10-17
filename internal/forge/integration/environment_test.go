package integration

import (
	"context"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/secret"
)

func TestParseServiceKind(t *testing.T) {
	t.Parallel()
	k, err := ParseDefinition(`
capability: sso
title: SSO
connector:
  fields:
    - key: url
      label: Issuer
resource:
  fields:
    - key: client_id
      label: Client ID
    - key: secret_from
      label: Secret name
      type: secretRef
      optional: true
env:
  - key: OIDC_ISSUER
    value: "{{connector.url}}"
steering: "Wire it."
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if k.Capability != "sso" || len(k.Resource.Fields) != 2 || k.Env[0].Key != "OIDC_ISSUER" {
		t.Fatalf("unexpected parse result: %+v", k)
	}

	cases := map[string]string{
		"capability: scm\ntitle: X":      "built in",
		"capability: Bad Slug\ntitle: X": "slug",
		"capability: ok":                 "title is required",
		"capability: ok\ntitle: X\nenv:\n  - key: \"A B\"\n    value: v": "not a valid variable",
	}
	for src, want := range cases {
		if _, err := ParseDefinition(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseDefinition(%q) error = %v, want containing %q", src, err, want)
		}
	}
}

func TestRenderServiceTemplate(t *testing.T) {
	t.Parallel()
	a := &Service{} // secretsAll not reached with resolveSecrets=false
	conn := map[string]any{"url": "https://auth.example"}
	res := map[string]any{"client_id": "waypoint", "secret_from": "MY_SECRET"}
	p := &project.Project{Slug: "waypoint", Name: "Waypoint"}

	got := a.RenderTemplate(context.Background(),
		"{{connector.url}}/o/{{resource.client_id}}/ svc={{project.slug}} key={{secret:resource.secret_from}}",
		conn, res, p, false)
	want := "https://auth.example/o/waypoint/ svc=waypoint key=MY_SECRET (secret, injected at runtime)"
	if got != want {
		t.Fatalf("render:\n got %q\nwant %q", got, want)
	}
	// Unresolved placeholders survive (callers use that to omit env lines).
	got = a.RenderTemplate(context.Background(), "{{connector.missing}}", conn, res, p, false)
	if got != "{{connector.missing}}" {
		t.Fatalf("unresolved placeholder rewritten: %q", got)
	}
}

// TestSupersedeWedgedLanding covers the engine carve-out: a landing run
// wedged on failed gates is auto-abandoned when a new run starts; a landing
// run with work still queued blocks as before.
func TestParseServiceKindRules(t *testing.T) {
	t.Parallel()
	good := `
capability: mailer
title: Mail Relay
description: sends mail
connector:
  fields:
    - {key: host, label: Host}
    - {key: token, label: Token, type: secretRef}
resource:
  fields:
    - {key: from_addr, label: From}
env:
  - {key: MAIL_HOST, value: "{{connector.host}}"}
`
	k, err := ParseDefinition(good)
	if err != nil {
		t.Fatalf("valid kind rejected: %v", err)
	}
	if k.Capability != "mailer" || k.Title != "Mail Relay" || len(k.Connector.Fields) != 2 {
		t.Fatalf("parsed kind wrong: %+v", k)
	}

	cases := []struct {
		name, yaml, want string
	}{
		{"bad-capability-slug", "capability: Not A Slug\ntitle: X", "slug"},
		{"reserved-capability", "capability: scm\ntitle: X", "built in"},
		{"missing-title", "capability: ok-thing", "title is required"},
		{"bad-field-key", "capability: ok\ntitle: X\nconnector:\n  fields:\n    - {key: \"1bad!\", label: L}", "field key"},
		{"bad-field-type", "capability: ok\ntitle: X\nconnector:\n  fields:\n    - {key: h, label: L, type: nope}", "unknown type"},
		{"bad-env-key", "capability: ok\ntitle: X\nenv:\n  - {key: \"BAD KEY\", value: v}", "not a valid variable name"},
		// Every visitor reads what a page is given; a secret is never one of those.
		{"browser-secret", "capability: ok\ntitle: X\nenv:\n  - {key: API_KEY, value: \"{{secret:resource.apiKey}}\", browser: true}", "cannot be given to browsers"},
		{"not-yaml", "::: not yaml :::", "yaml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseDefinition(c.yaml)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestRenderServiceTemplateSecretModes(t *testing.T) {
	t.Parallel()
	a := NewService(nil, Dependencies{Secrets: secret.NewService(nil, secret.Config{DataDir: t.TempDir()})})
	// A resource key that names a secret. With resolveSecrets=false the
	// reference name is shown; there is no live secret value to inject.
	resource := map[string]any{"apiKey": "SHOP_API_KEY"}
	ref := a.RenderTemplate(context.Background(), "KEY={{secret:resource.apiKey}}", nil, resource, nil, false)
	if !strings.Contains(ref, "SHOP_API_KEY (secret, injected at runtime)") {
		t.Fatalf("steering render should name the secret ref: %q", ref)
	}
	// resolveSecrets=true with no stored secret leaves the token unresolved
	// (the caller omits vars still carrying {{).
	live := a.RenderTemplate(context.Background(), "KEY={{secret:resource.apiKey}}", nil, resource, nil, true)
	if !strings.Contains(live, "{{secret:resource.apiKey}}") {
		t.Fatalf("no stored secret should leave the token unresolved: %q", live)
	}
}

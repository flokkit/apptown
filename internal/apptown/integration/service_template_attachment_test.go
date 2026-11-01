package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
)

func TestRenderServiceTemplateAndAttachedServices(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	id := createTestProject(t, db, "shop", "Shop")
	p, err := project.ByIDIn(context.Background(), db, id)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(db, Dependencies{Projects: project.New(db, nil)})
	spec := `capability: mailer
title: Mail Relay
connector:
  fields:
    - {key: host, label: Host}
env:
  - {key: MAIL_HOST, value: "{{connector.host}}"}
  - {key: MAIL_FROM, value: "{{project.slug}}@{{resource.domain}}"}
steering: "Send mail for {{project.name}} via {{connector.host}}."
guidance: "Use the mailer, do not print to stdout."`
	if err := s.ServiceKinds.Save(context.Background(), "mailer", spec, "Mail Relay", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Bindings.Save(context.Background(), "smtp", "mailer", "service", `{"host":"smtp.internal"}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.attach(t.Context(), id, "mailer", "smtp", `{"domain":"shop.example"}`, Provisioned, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	connCfg, err := s.ConnectorConfig(context.Background(), "smtp", "mailer")
	if err != nil || connCfg["host"] != "smtp.internal" {
		t.Fatalf("connectorConfigFor: %+v %v", connCfg, err)
	}
	if cfg, err := s.ConnectorConfig(context.Background(), "smtp", "wrong-cap"); err != nil || cfg != nil {
		t.Fatalf("wrong capability must yield no config: %v %v", cfg, err)
	}
	out := RenderSteering("{{project.slug}} {{connector.host}} {{resource.domain}}", connCfg, map[string]any{"domain": "shop.example"}, &p)
	if out != "shop smtp.internal shop.example" {
		t.Fatalf("render: %q", out)
	}
	svcs, err := s.AttachedServices(context.Background(), id)
	if err != nil || len(svcs) != 1 || svcs[0].Kind.Capability != "mailer" || svcs[0].Config["host"] != "smtp.internal" {
		t.Fatalf("attachedServices: %+v", svcs)
	}
	env := mustEnvironment(t, s, id)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "MAIL_HOST=smtp.internal") || !strings.Contains(joined, "MAIL_FROM=shop@shop.example") {
		t.Fatalf("integrationEnvFor: %v", env)
	}
}

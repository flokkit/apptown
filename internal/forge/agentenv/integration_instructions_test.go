package agentenv

import (
	"context"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

// Generated instructions name the attached kind, environment keys, rendered
// steering and unchanged guidance from the live Integration owner.
func TestIntegrationInstructionsNameAttachedService(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "shop", Name: "Shop"})
	})
	if err != nil {
		t.Fatal(err)
	}
	s := integration.NewService(db, integration.Dependencies{Projects: project.New(db, nil)})
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
	if err := s.Attachments.Attach(t.Context(), id, "mailer", "smtp", `{"domain":"shop.example"}`, integration.Provisioned, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	doc := NewInstructions(db, s).Integrations(t.Context(), id)
	if !strings.Contains(doc, "Mail Relay (attached, generated)") ||
		!strings.Contains(doc, "MAIL_HOST, MAIL_FROM") ||
		!strings.Contains(doc, "Send mail for Shop via smtp.internal.") ||
		!strings.Contains(doc, "do not print to stdout") {
		t.Fatalf("doc section:\n%s", doc)
	}
}

// A project that attached AI models is told how its app gets a key; one
// that did not is told nothing about it.
func TestIntegrationInstructionsSayHowAnAppGetsAIModels(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "shop", Name: "Shop"})
	})
	if err != nil {
		t.Fatal(err)
	}
	s := integration.NewService(db, integration.Dependencies{Projects: project.New(db, nil)})
	if strings.Contains(NewInstructions(db, s).Integrations(t.Context(), id), "AI models") {
		t.Fatal("an unattached project is told about AI models")
	}
	if _, err := s.Bindings.Save(context.Background(), "openrouter", integration.CapLLM, "openrouter", `{"provisioningKeyFrom":["OPENROUTER_PROVISIONING_KEY"],"maxLimit":5}`, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := s.Attachments.Attach(t.Context(), id, integration.CapLLM, "openrouter", "{}", integration.Entered, "fixture", nil); err != nil {
		t.Fatal(err)
	}
	doc := NewInstructions(db, s).Integrations(t.Context(), id)
	if !strings.Contains(doc, "AI models (attached, generated)") || !strings.Contains(doc, "OPENROUTER_API_KEY and OPENROUTER_BASE_URL") || !strings.Contains(doc, "needs: { llm:") {
		t.Fatalf("doc section:\n%s", doc)
	}
}

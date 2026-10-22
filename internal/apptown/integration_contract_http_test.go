package apptown

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestIntegrationHTTPProtectsConnectorAttachmentAndWiring(t *testing.T) {
	t.Parallel()
	a := authzFixtureWithApproval(t, workflow.ApprovalConfig{}, Config{PublicHost: "forge.test"})
	spec := `capability: mailer
title: Mail Relay
connector:
  fields:
    - {key: host, label: Host}
resource:
  fields:
    - {key: domain, label: Domain}
env:
  - {key: MAIL_HOST, value: "{{connector.host}}"}
`
	if err := a.Integrations.ServiceKinds.Save(context.Background(), "mailer", spec, "Mail Relay", "boss"); err != nil {
		t.Fatal(err)
	}
	defineWorkflows(t, a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "adopt-integration", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Stages: []schema.Stage{{Name: "review", Steps: []schema.Step{{Name: "accept", Uses: "approval"}}}}}})
	h := a.Handler()
	connector := `{"name":"smtp","capability":"mailer","provider":"service","config":{"host":"smtp.internal"}}`
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{"outsider", connector, 403}, {"boss", `{`, 400}, {"boss", `{"name":"smtp","capability":"unknown","provider":"service"}`, 400}, {"boss", connector, 200},
	} {
		w := authzCall(h, tc.who, "POST", "/api/v1/integrations", tc.body)
		if w.Code != tc.code {
			t.Fatalf("save connector: %d %s", w.Code, w.Body.String())
		}
	}
	b, err := a.Integrations.Bindings.ByName(t.Context(), "smtp")
	if err != nil || !strings.Contains(b.ConfigJSON, "smtp.internal") {
		t.Fatalf("saved connector: %+v %v", b, err)
	}
	w := authzCall(h, "owner", "GET", "/api/v1/integrations/catalog", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"capability":"mailer"`) {
		t.Fatalf("catalog: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "owner", "GET", "/api/v1/attachables", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"smtp"`) || !strings.Contains(w.Body.String(), `"domain"`) {
		t.Fatalf("attachables: %d %s", w.Code, w.Body.String())
	}
	path := "/api/v1/projects/game/integrations"
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{"outsider", `{"capability":"mailer","connector":"smtp","resource":{"domain":"game.test"}}`, 403},
		{"owner", `{`, 400},
		{"owner", `{"capability":"unknown","resource":{"x":"y"}}`, 404},
		{"owner", `{"capability":"mailer","connector":"absent","resource":{"domain":"game.test"}}`, 422},
		{"owner", `{"capability":"mailer","connector":"smtp","resource":{"extra":"x"}}`, 400},
		{"owner", `{"capability":"mailer","connector":"smtp","resource":{"domain":"game.test","extra":"x"}}`, 400},
	} {
		w := authzCall(h, tc.who, "POST", path, tc.body)
		if w.Code != tc.code {
			t.Fatalf("attach refusal: %d %s", w.Code, w.Body.String())
		}
		attached, err := a.Integrations.Attachments.IsAttached(t.Context(), idstest.N(1), "mailer")
		if err != nil || attached {
			t.Fatalf("rejected attachment persisted: %v %v", attached, err)
		}
	}
	for _, body := range []string{
		`{"capability":"mailer","connector":"smtp","resource":{"domain":"game.test"}}`,
		`{"capability":"errors","resource":{"dsn":"private-dsn"}}`,
		`{"capability":"feedback","resource":{"appKey":"private-app-key","url":"https://feedback.test"}}`,
	} {
		w := authzCall(h, "owner", "POST", path, body)
		if w.Code != 200 {
			t.Fatalf("attach: %d %s", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		who  string
		code int
	}{{"outsider", 403}, {"boss", 409}} {
		w := authzCall(h, tc.who, "DELETE", "/api/v1/integrations/smtp", "")
		if w.Code != tc.code {
			t.Fatalf("delete bound connector: %d %s", w.Code, w.Body.String())
		}
	}
	before, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		who, cap, body string
		code           int
	}{
		{"outsider", "mailer", `{"mode":"chat"}`, 403},
		{"owner", "missing", `{"mode":"chat"}`, 404},
		{"owner", "container-registry", `{"mode":"chat"}`, 422},
		{"owner", "mailer", `{`, 400},
		{"owner", "mailer", `{"mode":false}`, 400},
		{"owner", "mailer", `{"mode":"typo"}`, 400},
	} {
		w := authzCall(h, tc.who, "POST", path+"/"+tc.cap+"/wire", tc.body)
		if w.Code != tc.code {
			t.Fatalf("wire refusal (%s): %d %s", tc.body, w.Code, w.Body.String())
		}
		after, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
		if err != nil || len(after) != len(before) {
			t.Fatalf("refused wiring created work: %d %v", len(after), err)
		}
	}
	// Admission and the attachment link share one transaction: a failed link
	// is reported and leaves no Change behind for anyone to trip over.
	dbtest.FailNextStatement(t, a.DB, "-- name: LinkIntegrationWire")
	if w := authzCall(h, "owner", "POST", path+"/mailer/wire", `{"mode":"chat"}`); w.Code != 422 || !strings.Contains(w.Body.String(), "injected") {
		t.Fatalf("lost attachment link reported success: %d %s", w.Code, w.Body.String())
	}
	retained, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
	if err != nil || len(retained) != len(before) {
		t.Fatalf("link failure left admitted work: %+v %v", retained, err)
	}
	w = authzCall(h, "owner", "POST", path+"/mailer/wire", `{"mode":"chat"}`)
	var wired struct {
		Change workflow.Change
		Run    *workflow.Run
		URL    string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wired); err != nil || w.Code != 200 || wired.Change.ID.IsZero() || wired.Change.ProjectID != idstest.N(1) || wired.Change.Title != "Connect Mail Relay" || wired.Run != nil || !strings.HasSuffix(wired.URL, fmt.Sprintf("/changes/%d", wired.Change.ID)) {
		t.Fatalf("chat wiring: %d %s %v", w.Code, w.Body.String(), err)
	}
	// A retry while that wiring is live names it instead of opening another.
	if w := authzCall(h, "owner", "POST", path+"/mailer/wire", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), fmt.Sprintf("change %d already wires", wired.Change.Number)) {
		t.Fatalf("second live wiring: %d %s", w.Code, w.Body.String())
	}
	if after, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{}); err != nil || len(after) != len(before)+1 {
		t.Fatalf("refused wiring admitted work: %d %v", len(after), err)
	}
	w = authzCall(h, "owner", "GET", path, "")
	var out struct {
		Attachments []struct {
			Capability, Connector, Provenance string
			Resource                          map[string]string
			Wire                              *struct{ ChangeID ids.ID }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Attachments) != 3 || strings.Contains(w.Body.String(), "private-") {
		t.Fatalf("attachment projection: %d %s %v", w.Code, w.Body.String(), err)
	}
	found := false
	for _, att := range out.Attachments {
		if att.Capability == "mailer" {
			found = true
			if att.Connector != "smtp" || att.Provenance != "entered" || att.Resource["domain"] != "game.test" || att.Wire == nil || att.Wire.ChangeID != wired.Change.ID {
				t.Fatalf("mailer: %+v", att)
			}
		}
	}
	if !found {
		t.Fatal("mailer missing")
	}
	w = authzCall(h, "owner", "GET", "/api/v1/projects/open/integrations", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"attachments":[]}` {
		t.Fatalf("foreign project's attachments: %d %s", w.Code, w.Body.String())
	}
	w = authzCall(h, "owner", "POST", path+"/errors/wire", `{}`)
	var invoked struct {
		Change workflow.Change
		Run    *workflow.Run
	}
	if err := json.Unmarshal(w.Body.Bytes(), &invoked); err != nil || w.Code != 200 || invoked.Run == nil || invoked.Run.ChangeID != invoked.Change.ID || invoked.Run.WorkflowName != "adopt-integration" {
		t.Fatalf("default workflow wiring: %d %s %v", w.Code, w.Body.String(), err)
	}
	for _, tc := range []struct {
		who  string
		code int
	}{{"outsider", 403}, {"owner", 200}, {"owner", 404}} {
		w := authzCall(h, tc.who, "DELETE", path+"/mailer", "")
		if w.Code != tc.code {
			t.Fatalf("detach: %d %s", w.Code, w.Body.String())
		}
	}
	if _, err := a.Integrations.Bindings.ByName(t.Context(), "smtp"); err != nil {
		t.Fatalf("detach removed connector: %v", err)
	}
	for _, code := range []int{200, 404} {
		w := authzCall(h, "boss", "DELETE", "/api/v1/integrations/smtp", "")
		if w.Code != code {
			t.Fatalf("delete unused connector: %d %s", w.Code, w.Body.String())
		}
	}
}

// A model binding the inference gateway could not read is refused when it
// is saved, saying which field is wrong, and nothing is recorded: one saved
// with "concurrency":"2" would be accepted, then fail every step "invalid
// model binding". The connector asks for a number.
func TestAModelBindingTheGatewayCannotReadIsRefusedWhenSaved(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	for _, concurrency := range []string{`"2"`, `""`, `-1`} {
		w := authzCall(h, "boss", "POST", "/api/v1/integrations", `{"name":"gpu","capability":"model","provider":"pi","config":{"baseUrl":"https://gpu.example/v1","concurrency":`+concurrency+`}}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `\"concurrency\" must be`) {
			t.Fatalf("concurrency %s: %d %s", concurrency, w.Code, w.Body.String())
		}
		if _, err := a.Integrations.Bindings.ByName(t.Context(), "gpu"); err == nil {
			t.Fatalf("concurrency %s: the refused binding was recorded", concurrency)
		}
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/integrations", `{"name":"gpu","capability":"model","provider":"pi","config":{"baseUrl":"https://gpu.example/v1","concurrency":2}}`); w.Code != 200 {
		t.Fatalf("a whole number: %d %s", w.Code, w.Body.String())
	}
	b, err := a.Integrations.Bindings.ByName(t.Context(), "gpu")
	if err != nil {
		t.Fatal(err)
	}
	if cfg, err := b.ModelConfig(); err != nil || cfg.Concurrency != 2 {
		t.Fatalf("saved binding: %+v %v", cfg, err)
	}
	w := authzCall(h, "boss", "GET", "/api/v1/integrations/catalog", "")
	var catalog struct {
		Catalog []struct {
			Capability string
			Fields     []struct{ Key, Type string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || w.Code != 200 {
		t.Fatalf("catalog: %d %v", w.Code, err)
	}
	for _, c := range catalog.Catalog {
		for _, f := range c.Fields {
			if c.Capability == "model" && f.Key == "concurrency" && f.Type != "number" {
				t.Fatalf("the model connector's concurrency field is %q", f.Type)
			}
		}
	}
}

// AI models are attached by someone allowed to configure the project's
// integrations, naming a connector an administrator made; a person who can
// only deploy can neither attach them nor make or change the connector
// and its limits. Attaching takes no resource fields
// and calls no provider.
func TestAIModelsAreAttachedOnlyBySomeoneWhoConfiguresIntegrations(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	h := a.Handler()
	path := "/api/v1/projects/game/integrations"
	if w := authzCall(h, "owner", "POST", path, `{"capability":"llm"}`); w.Code != 422 {
		t.Fatalf("no connector yet: %d %s", w.Code, w.Body.String())
	}
	connector := `{"name":"openrouter","capability":"llm","provider":"openrouter","config":{"provisioningKeyFrom":"OPENROUTER_PROVISIONING_KEY","maxLimit":10}}`
	for _, who := range []string{"kid", "owner"} {
		if w := authzCall(h, who, "POST", "/api/v1/integrations", connector); w.Code != 403 {
			t.Fatalf("%s saved the connector: %d %s", who, w.Code, w.Body.String())
		}
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/integrations", strings.Replace(connector, "OPENROUTER_PROVISIONING_KEY", "STEP_OPENROUTER_KEY", 1)); w.Code != 400 {
		t.Fatalf("a STEP_ provisioning key: %d %s", w.Code, w.Body.String())
	}
	if w := authzCall(h, "boss", "POST", "/api/v1/integrations", connector); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		who, body string
		code      int
	}{
		{"kid", `{"capability":"llm"}`, 403},
		{"outsider", `{"capability":"llm"}`, 403},
		{"owner", `{"capability":"llm","connector":"absent"}`, 422},
		{"owner", `{"capability":"llm","resource":{"key":"x"}}`, 400},
	} {
		if w := authzCall(h, tc.who, "POST", path, tc.body); w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.who, tc.body, w.Code, w.Body.String())
		}
		if attached, err := a.Integrations.Attachments.IsAttached(t.Context(), idstest.N(1), "llm"); err != nil || attached {
			t.Fatalf("a refused attachment was recorded: %v %v", attached, err)
		}
	}
	if w := authzCall(h, "owner", "POST", path, `{"capability":"llm"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"connector":"openrouter"`) {
		t.Fatalf("attach: %d %s", w.Code, w.Body.String())
	}
	binding, resource, err := a.Integrations.Attachments.Attached(context.Background(), idstest.N(1), "llm")
	if err != nil || binding != "openrouter" || resource != "{}" {
		t.Fatalf("attached: %q %q %v", binding, resource, err)
	}
	w := authzCall(h, "owner", "GET", path, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"title":"AI models"`) {
		t.Fatalf("listing: %d %s", w.Code, w.Body.String())
	}
}

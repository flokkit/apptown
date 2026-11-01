package attention

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
)

type integrationEvidence struct{ health map[string]integration.Health }

func (s integrationEvidence) All(context.Context) ([]integration.Binding, error) {
	return []integration.Binding{{Name: "infisical"}, {Name: "registry", DisplayLabel: "Container images"}, {Name: "cloud"}}, nil
}
func (s integrationEvidence) RecordedHealth(_ context.Context, name string) (integration.Health, error) {
	return s.health[name], nil
}
func (s integrationEvidence) ProjectsByBinding(context.Context) (map[string][]integration.AttachedProject, error) {
	return map[string][]integration.AttachedProject{"registry": {{Slug: "calendar", Name: "Calendar"}}}, nil
}
func (s integrationEvidence) StandingByProvider(context.Context) (map[string][]deployment.ProviderDeployment, error) {
	return map[string][]deployment.ProviderDeployment{"registry": {{Project: "notes"}}}, nil
}

func TestIntegrationAttentionNamesAffectedAppsAndKeepsUnsetAndUnknownDistinct(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	evidence := integrationEvidence{health: map[string]integration.Health{
		"infisical": {StateLabel: integration.HealthNotSetUp},
		"registry":  {Automatic: true, StateLabel: integration.HealthFailed, Checked: true, CheckedAt: at.Format(time.RFC3339), Detail: "provider-secret-must-stay-on-connection-page", Stale: true},
		"cloud":     {StateLabel: integration.HealthFailed, Checked: true},
	}}
	got, err := (Integrations{Service: evidence, Bindings: evidence, Attachments: evidence, Deployments: evidence}).Snapshot(t.Context())
	if err != nil || len(got.Items) != 1 || len(got.Coverage) != 1 || !got.Known {
		t.Fatalf("snapshot: %+v %v", got, err)
	}
	item := got.Items[0]
	if item.Title != "Container images" || item.Href != "/integrations/registry" || !strings.Contains(item.Detail, "calendar, notes") || strings.Contains(item.Detail, "provider-secret") || !item.Stale || !item.ObservedAt.Equal(at) {
		t.Fatalf("failed integration: %+v", item)
	}
	for _, c := range got.Coverage {
		if c.Unknown || c.NextStep == "" || c.Href == "" || strings.Contains(c.Reason, "provider-secret") {
			t.Fatalf("coverage: %+v", c)
		}
	}
}

func TestExpiredIntegrationIsUnknownAndKindsWithoutChecksNeverCount(t *testing.T) {
	t.Parallel()
	evidence := integrationEvidence{health: map[string]integration.Health{
		"infisical": {Automatic: true, StateLabel: integration.HealthUnknown},
		"registry":  {Automatic: true, StateLabel: integration.HealthUnknown, LastResult: integration.HealthPassed, Expired: true, Stale: true},
		"cloud":     {StateLabel: integration.HealthFailed},
	}}
	got, err := (Integrations{Service: evidence, Bindings: evidence, Attachments: evidence, Deployments: evidence}).Snapshot(t.Context())
	if err != nil || len(got.Items) != 0 || !got.Coverage[1].Unknown || len(got.Coverage) != 2 {
		t.Fatalf("unknown coverage: %+v %v", got, err)
	}
}

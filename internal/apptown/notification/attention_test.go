package notification

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/attention"
	"github.com/flokkit/apptown/internal/apptown/dataprotection"
	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type attentionAuthority struct{ authority }

func (attentionAuthority) VisibleProjects(context.Context, access.Capability) ([]ids.ID, error) {
	return []ids.ID{idstest.N(2)}, nil
}

type attentionProject struct{}

func (attentionProject) ByID(context.Context, ids.ID) (project.Project, error) {
	return project.Project{Slug: "app"}, nil
}

type runtimeEvidence struct {
	rows []deployment.OperationalObservation
}

func (r *runtimeEvidence) Operational(context.Context, ids.ID) ([]deployment.OperationalObservation, error) {
	return r.rows, nil
}

type protectedDeployment struct{}

func (protectedDeployment) ForProject(context.Context, ids.ID) ([]*deployment.Deployment, error) {
	return []*deployment.Deployment{{ID: idstest.N(1), Kind: deployment.Standing, ObservedGeneration: 1, DataClass: "production", Name: "office"}}, nil
}

type interruptedProtection struct{ at time.Time }

func (p interruptedProtection) Snapshot(context.Context, ids.ID) (dataprotection.ProtectionSnapshot, error) {
	return dataprotection.ProtectionSnapshot{ObservedAt: p.at, OffHost: true, Items: []dataprotection.ProtectionItemObservation{{Key: "data", Need: "database", Copy: dataprotection.CopyFresh}}, Rehearsed: true, RehearsalOutcome: dataprotection.RehearsalUnknown, RehearsalAt: p.at, RestoreTested: true, RestoreOutcome: dataprotection.RehearsalUnknown, RestoreAt: p.at}, nil
}

type staleIntegration struct{ at time.Time }

func (staleIntegration) All(context.Context) ([]integration.Binding, error) {
	return []integration.Binding{{Name: "registry"}}, nil
}
func (s staleIntegration) RecordedHealth(context.Context, string) (integration.Health, error) {
	return integration.Health{Automatic: true, StateLabel: integration.HealthFailed, Checked: true, CheckedAt: s.at.Format(time.RFC3339), Stale: true}, nil
}
func (staleIntegration) ProjectsByBinding(context.Context) (map[string][]integration.AttachedProject, error) {
	return map[string][]integration.AttachedProject{}, nil
}
func (staleIntegration) StandingByProvider(context.Context) (map[string][]deployment.ProviderDeployment, error) {
	return map[string][]deployment.ProviderDeployment{}, nil
}
func TestOwnerUncertaintySurvivesAttentionReadIntoDigest(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 8)
	f.evaluate(t) // Establish a healthy empty baseline before uncertain items arrive.
	stale := staleIntegration{f.now}
	sources := attention.Sources{Protection: attention.Protection{Projects: attentionProject{}, Deployments: protectedDeployment{}, Observer: interruptedProtection{f.now}, Now: func() time.Time { return f.now }}, Integrations: attention.Integrations{Service: stale, Bindings: stale, Attachments: stale, Deployments: stale}}
	reader := attention.New(&attentionAuthority{}, sources)
	f.s.d.ReadAttention = reader.Read
	f.evaluate(t)
	f.evaluate(t)
	if len(f.deliveries(t)) != 0 {
		t.Fatal("interrupted recovery or retained stale failure paged")
	}
	f.now = f.now.Add(time.Hour)
	f.evaluate(t)
	rs := f.deliveries(t)
	if len(rs) != 1 {
		t.Fatalf("uncertainty digest: %+v", rs)
	}
	var m Message
	if err := json.Unmarshal(rs[0].Body, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 3 {
		t.Fatalf("missing uncertain items: %+v", m.Items)
	}
	for _, i := range m.Items {
		if i.Section != "Not known" {
			t.Fatalf("uncertain result claimed confirmed: %+v", i)
		}
	}
}
func TestRestartWithPreActRuntimeReadingCannotResolve(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.channel(t, "phone", 8)
	evidence := &runtimeEvidence{}
	reader := attention.New(&attentionAuthority{}, attention.Sources{Deployments: attention.Deployments{Projects: attentionProject{}, Runtime: evidence, Now: func() time.Time { return f.now }}})
	f.s.d.ReadAttention = reader.Read
	f.evaluate(t)
	evidence.rows = []deployment.OperationalObservation{{ID: idstest.N(1), Name: "office", ObservedGeneration: 1, DesiredGeneration: 1, RuntimeState: "running", LastApplied: &deployment.AppliedAttempt{At: f.now.Add(-time.Hour), Generation: 1}, Runtime: deployment.RuntimeObservation{State: deployment.RuntimeRunning, Health: "unhealthy", AttemptedAt: f.now, ObservedAt: f.now}}}
	f.evaluate(t)
	if len(f.deliveries(t)) != 1 {
		t.Fatal("fresh unhealthy runtime did not announce")
	}
	f.now = f.now.Add(time.Minute)
	evidence.rows[0].LastActAt = f.now
	f.now = f.now.Add(time.Minute)
	f.s = New(f.s.d)
	f.evaluate(t)
	if len(f.deliveries(t)) != 1 {
		t.Fatal("restart's withheld reading resolved an unchanged-generation item")
	}
	evidence.rows[0].Runtime = deployment.RuntimeObservation{State: deployment.RuntimeRunning, Health: "healthy", AttemptedAt: f.now, ObservedAt: f.now}
	f.evaluate(t)
	rs := f.deliveries(t)
	if len(rs) != 2 || rs[0].Kind != "resolved" {
		t.Fatalf("fresh healthy observation did not resolve: %+v", rs)
	}
	if !strings.Contains(string(rs[0].Body), "https://apps.example/") {
		t.Fatal("public origin missing from notice")
	}
}

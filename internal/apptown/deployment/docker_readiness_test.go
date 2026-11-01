package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

type maintainedSources struct {
	requestSources
	resolved  string
	requested string
}

func (s *maintainedSources) Contract(_ context.Context, _ ReleaseProject, revision string) (*appcontract.Contract, string, error) {
	s.requested = revision
	return s.contract, s.resolved, s.err
}

// The owner reads the admitted generation; maintenance does not impose a new
// cold-boot budget using its slower default cadence.
func TestDockerMaintenanceReadsSelectedSourceWithoutReadmittingBootFit(t *testing.T) {
	s, in, _, _ := requestFixture(t)
	dep, err := s.Deployments.ByName(t.Context(), in.ProjectID, in.DeploymentName)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(docker.SourceInputs{Version: 1, Revision: "selected"})
	artifact := deploy.ConfigHash(string(raw))
	at, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "maintained", Inputs: Inputs{Revision: "selected", DockerSource: string(raw), ArtifactHash: artifact}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", Health: "healthy", ImageDigest: "sha256:selected"}}); err != nil {
		t.Fatal(err)
	}
	dep, err = s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	part := appcontract.Part{Name: "web", Public: true, Port: 8080, Intent: &appcontract.Health{Readiness: &appcontract.Probe{HTTP: "/ready", SuccessThreshold: 8}}}
	contract := &appcontract.Contract{Parts: []appcontract.Part{part}}
	if err := docker.RefusesHealth(contract); err != nil {
		t.Fatalf("cold admission rejected valid policy: %v", err)
	}
	source := &maintainedSources{requestSources: requestSources{contract: contract}, resolved: "selected"}
	s.release.Sources = source
	sentinel := errors.New("sampler reached")
	calls := 0
	driver := &docker.Driver{Allowed: docker.AuthorisedWhenRequested, Scope: func(context.Context) (docker.Scope, error) { calls++; return docker.Scope{}, sentinel }}
	d := dockerReadiness{driver: driver, service: s, record: listingRecord{deployment: dep}}
	p := deploy.Placement{ID: dep.ID, Generation: at.Generation, HostPort: 8080, Label: "selected", Name: "main", URL: "http://selected.example"}
	names, err := docker.NamesOf(p.ID, p.Name)
	if err != nil {
		t.Fatal(err)
	}
	p.Container = names.App()
	out, err := d.maintainReadiness(t.Context(), p, artifact, func(context.Context) error { return nil })
	if !errors.Is(err, sentinel) || calls != 1 || source.requested != "selected" {
		t.Fatalf("selected sampler calls=%d revision=%q error=%v", calls, source.requested, err)
	}
	if out.ReadinessPolicy == nil || out.ReadinessPolicy.SuccessThreshold != 8 || out.ReadinessPolicy.Period != 15*time.Second {
		t.Fatalf("lost maintenance policy: %+v", out)
	}
	source.resolved = "different"
	_, err = d.maintainReadiness(t.Context(), p, artifact, func(context.Context) error { return nil })
	if err == nil || calls != 1 {
		t.Fatalf("mismatched source probed: calls=%d err=%v", calls, err)
	}
	source.resolved = "selected"
	source.err = errors.New("selected source unavailable")
	_, err = d.maintainReadiness(t.Context(), p, artifact, func(context.Context) error { return nil })
	if err == nil || calls != 1 {
		t.Fatalf("unreadable source probed: calls=%d err=%v", calls, err)
	}
}

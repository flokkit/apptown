package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// dockerReadiness resolves only the source admitted for the settled generation.
// The Docker library owns runtime/probe identity; this owner retains selection.
type dockerReadiness struct {
	driver  *docker.Driver
	service *Service
	record  listingRecord
}

func (d dockerReadiness) maintainReadiness(ctx context.Context, p deploy.Placement, artifact string, allowed func(context.Context) error) (deploy.Observation, error) {
	unknown := deploy.Observation{Generation: p.Generation, Health: "unknown"}
	s := d.service
	if s.release.Sources == nil || d.record.deployment.ImageDigest == "" {
		return unknown, errors.New("Docker readiness lacks retained source or image authority")
	}
	in, err := s.Outcomes.InputsAtGeneration(ctx, p.ID, p.Generation)
	if err != nil {
		return unknown, err
	}
	var selected docker.SourceInputs
	if err := json.Unmarshal([]byte(in.DockerSource), &selected); err != nil {
		return unknown, errors.New("Docker readiness cannot read retained source")
	}
	if selected.Version != 1 || selected.Revision == "" || selected.Revision != in.Revision || selected.Revision != d.record.deployment.ReleaseSHA || in.ArtifactHash != artifact || deploy.ConfigHash(in.DockerSource) != artifact {
		return unknown, errors.New("Docker readiness retained source identity differs")
	}
	project, err := s.release.Sources.Project(ctx, d.record.deployment.ProjectID)
	if err != nil {
		return unknown, err
	}
	contract, resolved, err := s.release.Sources.Contract(ctx, project, selected.Revision)
	if err != nil {
		return unknown, err
	}
	if resolved != selected.Revision {
		return unknown, errors.New("Docker readiness source resolved a different revision")
	}
	if contract == nil || contract.PublicPart() == nil {
		return unknown, errors.New("Docker readiness selected source has no public application")
	}
	part := contract.PublicPart()
	var probe *appcontract.Probe
	if part.Intent != nil {
		probe = part.Intent.Readiness
	}
	policy, err := appcontract.NormalizeHealthPolicy(probe, "readiness", appcontract.HealthPolicy{Period: 15 * time.Second, Timeout: 2 * time.Second, FailureThreshold: 1, SuccessThreshold: 1})
	if err != nil {
		return unknown, err
	}
	driver := *d.driver
	driver.Now = s.clock
	out, err := driver.Sample(ctx, docker.ReadinessSample{Placement: p, Artifact: artifact, Image: d.record.deployment.ImageDigest, Part: part, Timeout: policy.Timeout}, allowed)
	out.ReadinessPolicy = &policy
	return out, err
}

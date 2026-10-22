package deployment

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// A prepared landing executor owns one boot and its evidence. It has no SQL,
// workflow dispatch or App reference; the caller supplies boot and URL policy.
type landingDeploymentExecution struct {
	label    deploy.HostLabel // the deployment's name on its host
	target   docker.BootRequest
	contract *appcontract.Contract
	artifact string
	boot     func(context.Context, docker.BootRequest, *appcontract.Contract) (docker.BootResult, error)
	url      func(int) string
	result   LandingBoot
}
type LandingBoot struct {
	Boot docker.BootResult
	Err  error
	URL  string
}

func (e *landingDeploymentExecution) execute(ctx context.Context, at Attempt, checkpoint deploy.Checkpoint) (deploy.Observation, error) {
	e.result = LandingBoot{}
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	identity := deploy.DockerIdentity{Deployment: e.label, Generation: at.Generation, Artifact: e.artifact}
	if err := checkpoint(identity.Checkpoint(e.target.Name)); err != nil {
		return deploy.Observation{}, deploy.Terminal(fmt.Errorf("recording deployment identity: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	target := e.target
	target.Identity = &identity
	e.result.Boot, e.result.Err = e.boot(ctx, target, e.contract)
	e.result.URL = e.url(e.result.Boot.HostPort)
	if err := ctx.Err(); err != nil {
		return deploy.Observation{}, err
	}
	if e.result.Err != nil {
		// Terminal is a confirmed outcome: nothing started, or it started and
		// is known to have failed. An unreadable browser setting is the first,
		// so it never strands the deployment as uncertain.
		be := docker.AsBootError(e.result.Err)
		if be.BeforeEffects() || be.Phase == docker.PhaseProbe || be.Phase == docker.PhasePage {
			return deploy.Observation{}, deploy.Terminal(e.result.Err)
		}
		return deploy.Observation{}, e.result.Err
	}
	o, err := docker.RecordBoot(ctx, e.target.Docker, identity, e.target.Name, e.result.Boot.Image(),
		e.result.URL, e.target.HostPort, checkpoint)
	if err != nil {
		return o, fmt.Errorf("verifying the running deployment: %w", err)
	}
	return o, nil
}

type LandingEvidence struct {
	URL           string                 `json:"url"`
	Deployment    string                 `json:"deployment"`
	Container     string                 `json:"container"`
	Secrets       []docker.SecretReceipt `json:"secrets,omitempty"`
	SecretProfile string                 `json:"secretProfile,omitempty"`
	Image         *string                `json:"image,omitempty"`
	ImageID       string                 `json:"imageId,omitempty"`
	ContractImage string                 `json:"contractImage,omitempty"`
	BuiltFrom     string                 `json:"builtFrom,omitempty"`
	Digest        *string                `json:"digest,omitempty"`
	// Bundle is the digest bundle the landing committed and ran, when it
	// was built on the worker.
	Bundle ids.ID `json:"bundle,omitzero"`
	// FrontPage is what the front-page check saw when the deploy passed it.
	FrontPage *docker.PageWitness `json:"frontPage,omitempty"`
}

func (e *landingDeploymentExecution) evidence(ctx context.Context) LandingEvidence {
	depEv := LandingEvidence{URL: e.result.URL, Deployment: e.target.Deployment, Container: e.target.Name, FrontPage: e.result.Boot.Page}
	if len(e.result.Boot.Stack.Secrets.Receipts) > 0 {
		// Which secrets reached which parts, from which store, at which
		// version — the receipt; never a value.
		depEv.Secrets = e.result.Boot.Stack.Secrets.Receipts
		depEv.SecretProfile = e.result.Boot.Stack.Secrets.Profile
	}
	// Record the actual image identity separately from its declared source.
	if running := e.target.Docker.Text(ctx, "inspect", "-f", "{{.Config.Image}}", e.target.Name); running != "" {
		depEv.Image = &running
		if id := e.target.Docker.Text(ctx, "inspect", "-f", "{{.Image}}", e.target.Name); id != "" {
			depEv.ImageID = id
		}
		if e.contract.PublicPart().Image != "" && e.contract.PublicPart().Image != running {
			depEv.ContractImage = e.contract.PublicPart().Image
		}
		if e.contract.PublicPart().Build != "" {
			depEv.BuiltFrom = e.contract.PublicPart().Build
		}
		if d := e.target.Docker.ImageDigest(ctx, running); d != "" {
			depEv.Digest = &d
		}
	} else if e.result.Boot.Image() != "" {
		image, digest := e.result.Boot.Image(), e.target.Docker.ImageDigest(ctx, e.result.Boot.Image())
		depEv.Image, depEv.Digest = &image, &digest
	}
	return depEv
}

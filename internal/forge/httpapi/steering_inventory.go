package httpapi

import (
	"context"

	"flokkit.io/forge/internal/ids"
)

// SteeringSources describes live agent-facing facts, shared with execution.
type SteeringSources interface {
	Registry(ctx context.Context) string
	Integrations(context.Context, ids.ID) string
}

type DelegationSource interface {
	Delegation(ctx context.Context) string
}

// PlatformSteering is immutable platform text supplied by composition.
type PlatformSteering struct{ Complaints, Hygiene, Runtime, Docker string }
type SteeringInventory struct {
	source     SteeringSources
	delegation DelegationSource
	text       PlatformSteering
}

func NewSteeringInventory(source SteeringSources, delegation DelegationSource, text PlatformSteering) SteeringInventory {
	return SteeringInventory{source: source, delegation: delegation, text: text}
}

type steeringSection struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	BodyMD string `json:"bodyMd,omitempty"`
	Note   string `json:"note"`
	Bytes  int    `json:"bytes"`
	// Always: in every agent step on the project. Otherwise only steps of
	// the shape the note names get it.
	Always bool `json:"always"`
}

// sections lists the machine-written sections with their sizes.
// projectID 0 gives the instance-level inventory (what any project would
// carry); a project adds its own integrations section. Empty sections are
// left out: what is not written costs nothing.
func (s SteeringInventory) sections(ctx context.Context, projectID ids.ID) []steeringSection {
	var out []steeringSection
	add := func(s steeringSection) {
		if s.BodyMD == "" {
			return
		}
		s.Bytes = len(s.BodyMD)
		out = append(out, s)
	}
	add(steeringSection{Name: "platform/complaints-box", Source: "AppTown", Always: true, BodyMD: s.text.Complaints,
		Note: "First in every agent step's steering: platform friction gets reported to the complaints box, even with a workaround."})
	add(steeringSection{Name: "ambient/registries", Source: "registry bindings", Always: true, BodyMD: s.source.Registry(ctx),
		Note: "Injected into every agent step's steering after the pinned guidance, and refreshed into console steering on each provision. The exact text each step received is attached to the step as a 'steering' artifact (sha256 in evidence)."})
	if !projectID.IsZero() {
		add(steeringSection{Name: "ambient/integrations", Source: "this project's integrations and attached services", Always: true,
			BodyMD: s.source.Integrations(ctx, projectID),
			Note:   "What this project has attached (error tracker, feedback, databases) and how the app is expected to use it."})
	}
	add(steeringSection{Name: "platform/workspace-hygiene", Source: "AppTown", Always: true, BodyMD: s.text.Hygiene,
		Note: "Keeps agents out of .apptown/sessions, whose transcripts would flood their context."})
	add(steeringSection{Name: "platform/delegation", Source: "model slots", BodyMD: s.delegation.Delegation(ctx),
		Note: "Only steps that run in the container executor: which models the agent may hand work to."})
	add(steeringSection{Name: "platform/runtime-contract", Source: "AppTown", BodyMD: s.text.Runtime,
		Note: "Only steps with the implementation skill: how apps deploy (cold containers, mise pins, the boot gate)."})
	add(steeringSection{Name: "platform/docker-in-docker", Source: "AppTown", BodyMD: s.text.Docker,
		Note: "Only steps that build container images: the private Docker daemon and the org registry."})
	return out
}

// steeringOverhead sums the generated sections: what every step carries,
// and the most a step of every shape would carry on top.
func steeringOverhead(secs []steeringSection) (always, conditional int) {
	for _, s := range secs {
		if s.Always {
			always += s.Bytes
		} else {
			conditional += s.Bytes
		}
	}
	return
}

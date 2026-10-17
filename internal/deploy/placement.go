package deploy

import "flokkit.io/forge/internal/ids"

// Placement is a standing deployment as a driver sees it: what it is
// called and what the provider last said about it. Forge keeps a record
// of a deployment; a driver is handed this. Whose it is, the driver never
// asks: its collaborators were built knowing.
type Placement struct {
	ID         ids.ID         // the deployment's record identity: need records; opaque to the driver
	Label      HostLabel      // the deployment's name on its host: container labels, boot proofs
	Name       string         // the deployment's name (main, cloud, preview-12)
	Generation int64          // the generation last observed
	URL        string         // where it was last reached
	Handle     map[string]any // the provider's own identifiers
	Container  string         // the built-in host's container, when it has one
	HostPort   int            // the built-in host's port, when it has one
	// Gate, when set, puts the deployment behind the forge's access gate
	// (design/subsystems/deployment.md, "Restored copies"): it holds production data
	// that only people the forge admits may reach.
	Gate *Gate
}

// Gate is the forge's access gate in front of a deployment. The gate is
// its only public component; the app's public part runs privately behind
// it, and nothing else of the app starts. A driver that cannot place a
// gate refuses a gated placement rather than expose the app.
//
// A gated deployment's observation never asks the front door anonymously
// (the gate answers that with 404), and never copies the platform's
// component logs into the forge's records: they can hold production rows.
// The forge's own checks reach the app through the gate with the secret.
type Gate struct {
	// Image is the gate's image by digest, published for the forge.
	Image string
	// Secret is this deployment's gate secret: the probe token and the
	// gate's credential when it redeems a ticket. It is held only in
	// memory while the forge starts the deployment, and is delivered as the
	// provider's encrypted secret to the gate alone. Empty when the forge
	// only observes or takes the deployment down, which needs no secret.
	Secret string
	// Forge is the forge's public origin, where the gate redeems tickets.
	Forge string
	// Marker is this deployment's own random mark, recorded by the forge
	// before anything is made and rendered into the gate's environment. It
	// is what proves an app found on the account by its name is this
	// deployment's and not another's of the same name: a take-down deletes
	// an app it did not record only when the app carries this mark.
	Marker string
}

// Gate environment, as the gate (cmd/forge-gate) reads it.
const (
	GateEnvUpstream = "GATE_UPSTREAM"
	GateEnvSecret   = "GATE_SECRET"
	GateEnvForge    = "GATE_FORGE"
	GateEnvListen   = "GATE_LISTEN"
	// GateEnvMarker carries Gate.Marker in the gate's environment; the gate
	// does not read it.
	GateEnvMarker = "FORGE_GATED_DEPLOYMENT"
	// GatePort is the port the gate listens on.
	GatePort = 8080
	// GateProbeHeader carries the gate secret on the forge's own checks.
	GateProbeHeader = "Forge-Restore-Token"
)

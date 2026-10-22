package deploy

import "github.com/flokkit/apptown/internal/ids"

// Placement is a standing deployment as a driver sees it: what it is
// called and what the provider last said about it. AppTown keeps a record
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
	// HostPrefix is what the deployment's names on a Docker host or a
	// Portainer environment begin with, recorded when it was made and never
	// changed: what it made is always found under the names it made it with.
	HostPrefix string
	// Gate, when set, puts the deployment behind AppTown's access gate
	// (design/subsystems/deployment.md, "Restored copies"): it holds production data
	// that only people AppTown admits may reach.
	Gate *Gate
}

// Gate is AppTown's access gate in front of a deployment. The gate is
// its only public component; the app's public part runs privately behind
// it, and nothing else of the app starts. A driver that cannot place a
// gate refuses a gated placement rather than expose the app.
//
// A gated deployment's observation never asks the front door anonymously
// (the gate answers that with 404), and never copies the platform's
// component logs into AppTown's records: they can hold production rows.
// AppTown's own checks reach the app through the gate with the secret.
type Gate struct {
	// Image is the gate's image by digest, published for AppTown.
	Image string
	// Secret is this deployment's gate secret: the probe token and the
	// gate's credential when it redeems a ticket. It is held only in
	// memory while AppTown starts the deployment, and is delivered as the
	// provider's encrypted secret to the gate alone. Empty when AppTown
	// only observes or takes the deployment down, which needs no secret.
	Secret string
	// AppTown is AppTown's public origin, where the gate redeems tickets.
	AppTown string
	// Marker is this deployment's own random mark, recorded by AppTown
	// before anything is made and rendered into the gate's environment. It
	// is what proves an app found on the account by its name is this
	// deployment's and not another's of the same name: a take-down deletes
	// an app it did not record only when the app carries this mark.
	Marker string
}

// Gate environment, as the gate (cmd/apptown-gate) reads it.
const (
	GateEnvUpstream = "GATE_UPSTREAM"
	GateEnvSecret   = "GATE_SECRET"
	GateEnvAppTown  = "GATE_APPTOWN"
	GateEnvListen   = "GATE_LISTEN"
	// GateEnvMarker carries Gate.Marker in the gate's environment; the gate
	// does not read it.
	GateEnvMarker = "APPTOWN_GATED_DEPLOYMENT"
	// GatePort is the port the gate listens on.
	GatePort = 8080
	// GateProbeHeader carries the gate secret on AppTown's own checks.
	GateProbeHeader = "AppTown-Restore-Token"
)

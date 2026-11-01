package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// Host is the built-in Docker host (architecture.md §5.6, §11): the one
// boot sequence every path runs (previews, landings, releases, rebuilds),
// with what it needs of the world declared and supplied at construction.
// A standalone tool builds one from its own values; AppTown builds one over
// its secrets broker, dependency catalog, registry connectors, attached
// integrations and routes.
type Host struct{ deps Dependencies }

// Dependencies is what the host asks of the world; nil for Registry,
// Integrations or Routes means "none", never a crash. A caller that serves
// many owners builds a host per owner: the collaborators already know
// whose secrets, registries and integrations they answer for.
type Dependencies struct {
	Secrets      SecretResolver
	Needs        needRegistry
	Registry     registryLogin
	Integrations contributions
	Routes       Routes
	Probe        prober // nil is the default probe
	// The images a boot runs for itself, from the tool versions snapshot its
	// caller's unit of work took: the file server for static parts, an image
	// with chown for handing a volume to the app's user ("" skips it), and
	// the df a new boot's disk admission runs.
	StaticServer string
	ChownImage   string
	DFImage      string
}

func NewHost(deps Dependencies) *Host { return &Host{deps: deps} }

// SecretResolver resolves the app's declared secrets for one boot: per
// consumer, the values to inject, and the receipts (never values) the
// boot records.
// A resolver that registers credentials calls allowed again before each
// registration; nil carries no additional check. Boot checks before resolution.
type SecretResolver interface {
	Resolve(ctx context.Context, slug, deployment, ws string, c *appcontract.Contract, allowed func(context.Context) error) (Delivery, error)
}

// Delivery is the broker's answer for one boot.
type Delivery struct {
	Env      func(consumer string) map[string]string
	Receipts []SecretReceipt
	Profile  string
}

// EnvFor is what a part, an init step or a renderer asks of a delivery.
func (d Delivery) EnvFor(consumer string) map[string]string {
	if d.Env == nil {
		return nil
	}
	return d.Env(consumer)
}

// needRegistry fulfils the contract's needs locally: emulator containers
// and the env the app reads to use them; Data is where an emulator keeps
// its data, "" for one that keeps none. A kind that cannot be read is an
// error, never "keeps none": a standing service remade without its data
// folder would start empty.
type needRegistry interface {
	// Fulfil refuses a need it cannot fulfil as declared with the sentence
	// the release and preview show (an unknown need, a withdrawn
	// declaration).
	Fulfil(ctx context.Context, needs []string, params map[string]map[string]string) (services []deploy.ServiceSpec, env map[string]string, err error)
	Data(ctx context.Context, capability string) (string, error)
}

// registryLogin logs the daemon into the registry an image is pulled from.
// A login that fails, or a credential that cannot be read, is an error.
type registryLogin interface {
	Login(ctx context.Context, dk Scope, imageRef string) error
}

// contributions is the attached integrations' rendered env for an app, and
// the part of it safe for any browser to read, as a JSON object. A read
// that fails is an error, never fewer variables.
type contributions interface {
	EnvFor(context.Context) ([]string, error)
	BrowserEnv(context.Context) (string, error)
}

// Routes is how an app is reached: the front-door headers a probe sends,
// and the origins a browser reaches the app at.
type Routes interface {
	FrontDoorHeaders() map[string]string
	PublicOrigins(ctx context.Context) string
}

// prober answers "is the app actually up?" for a container at a URL.
type prober interface {
	Check(ctx context.Context, dk Scope, container, url string, timeout time.Duration, probe renderedProbe) (bool, string)
}

func (h *Host) staticServer() (string, error) {
	if h.deps.StaticServer == "" {
		return "", fmt.Errorf("no static server image: the tool versions name it")
	}
	return h.deps.StaticServer, nil
}
func (h *Host) chownImage() string { return h.deps.ChownImage }
func (h *Host) login(ctx context.Context, dk Scope, image string) error {
	if h.deps.Registry == nil {
		return nil
	}
	return h.deps.Registry.Login(ctx, dk, image)
}
func (h *Host) origins(ctx context.Context) string {
	if h.deps.Routes == nil {
		return ""
	}
	return h.deps.Routes.PublicOrigins(ctx)
}
func (h *Host) frontDoor() map[string]string {
	if h.deps.Routes == nil {
		return nil
	}
	return h.deps.Routes.FrontDoorHeaders()
}
func (h *Host) contributions(ctx context.Context) ([]string, error) {
	if h.deps.Integrations == nil {
		return nil, nil
	}
	return h.deps.Integrations.EnvFor(ctx)
}
func (h *Host) browserEnv(ctx context.Context) (string, error) {
	if h.deps.Integrations == nil {
		return "{}", nil
	}
	return h.deps.Integrations.BrowserEnv(ctx)
}
func (h *Host) prober() prober {
	if h.deps.Probe != nil {
		return h.deps.Probe
	}
	return &Probe{}
}

package deployment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/oci"
	"github.com/flokkit/apptown/internal/strs"
)

// MemoryDefaults reads the project owner's pinned primary publication.
type MemoryDefaults interface {
	MemoryDefault(context.Context, ids.ID) (appcontract.MemoryDefault, string, int64, error)
}

// Environment is what a selected app's boot is given, wherever it boots:
// its secrets, its dependencies and emulators, the values its registered
// needs inject (an AI models key among them), the registry credentials its
// images are pulled with, the attached integrations' contributions and the
// addresses it is reached at (architecture.md §5.6, §11). It holds the
// eval rule those addresses and credentials follow: an eval copy is served
// on the eval domain and publishes to its own repository (evals.go).
//
// One instance serves Deployment's releases and copies and Preview's boots.
// It keeps no state: the owners it reads answer for their own records.
type Environment struct {
	memoryDefaults MemoryDefaults
	targets        *Targets
	secrets        *secret.Service
	integrations   *integration.Service
	needs          *dependency.Catalog
	probe          *docker.Probe
	chownImage     string
	routes         Routing
	projects       *project.Projects
}

// EnvironmentDependencies are the owners and settings a boot's environment
// is prepared from.
type EnvironmentDependencies struct {
	MemoryDefaults MemoryDefaults
	Secrets        *secret.Service
	Integrations   *integration.Service
	Needs          *dependency.Catalog
	// Probe is this machine's readiness probe; ChownImage the image that
	// gives a data volume to its app's user.
	Probe      *docker.Probe
	ChownImage string
	Routes     Routing
	// Projects answers each project's purpose (project/purpose.go), which
	// decides its domain and the credentials it is given. Without it every
	// purpose question is an error and the effect is refused.
	Projects *project.Projects
}

// NewEnvironment prepares boots from deps; the targets are read for the
// intranet's domain, among the origins an app trusts.
func NewEnvironment(targets *Targets, deps EnvironmentDependencies) *Environment {
	if deps.Routes.Scheme == "" {
		deps.Routes.Scheme = "http"
	}
	return &Environment{memoryDefaults: deps.MemoryDefaults, targets: targets, secrets: deps.Secrets, integrations: deps.Integrations, needs: deps.Needs,
		probe: deps.Probe, chownImage: deps.ChownImage, routes: deps.Routes, projects: deps.Projects}
}

// Routes is how this AppTown names addresses, as configured; RoutesFor is
// one project's.
func (e *Environment) Routes() Routing { return e.routes }

// Probe is this machine's readiness probe.
func (e *Environment) Probe() *docker.Probe { return e.probe }

// errNoPurposes: an environment built without the project record cannot
// say what a project is for, so it refuses rather than guess "ordinary".
var errNoPurposes = errors.New("this AppTown's deployment environment cannot read project purposes")

// Purpose is Project's purpose for projectID. A project Project cannot
// read, or an environment without the record, is an error, never ordinary.
func (e *Environment) Purpose(ctx context.Context, projectID ids.ID) (project.Purpose, error) {
	if e.projects == nil {
		return "", errNoPurposes
	}
	return e.projects.Purpose(ctx, projectID)
}

// RoutesFor is how a project's addresses are named: a copy's on the eval
// domain, every other project's as configured. A failed read names the
// ordinary addresses, which the host router then refuses to serve a copy
// at; it is logged.
func (e *Environment) RoutesFor(ctx context.Context, projectID ids.ID) Routing {
	purpose, err := e.Purpose(ctx, projectID)
	if err != nil {
		log.Printf("project %d: asking its purpose for its address: %v", projectID, err)
	}
	return e.routes.ForProject(purpose.EvalDomainOnly())
}

func (e *Environment) FrontDoorHeaders() map[string]string {
	return map[string]string{"X-Forwarded-Proto": e.routes.Scheme}
}

func (e *Environment) PublicOrigins(ctx context.Context) string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		if !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	r := e.routes
	if r.PreviewDomain != "" {
		add(r.PreviewScheme + "://*." + r.PreviewDomain)
	}
	if r.MainDomain != "" {
		add(r.MainScheme + "://*." + r.MainDomain)
	}
	if r.EvalDomain != "" {
		add(r.EvalScheme + "://*." + r.EvalDomain)
	}
	if t, err := e.targets.ByName(ctx, IntranetTargetName); err == nil && t.Status.Available() == nil {
		if domain, _ := t.Ingress["domain"].(string); domain != "" {
			add("https://*." + domain)
		}
	}
	return strings.Join(out, ",")
}

// Host is the built-in Docker host for one project, built from its actual
// owners: its secrets broker, the dependency catalog and its emulators, the
// registry connectors, the attached integrations' contributions and the
// routes. The collaborators close over the project, so the host never asks
// whose app it is booting.
//
// creds are the needs' generated credentials of the deployment or the
// preview being booted (NeedCredentials). keys is who an AI models key is
// made for when the app needs one (the same owner, by the project's slug);
// nil makes none, for a caller that only reads what a boot would be.
func (e *Environment) Host(projectID ids.ID, creds dependency.Credentials, keys func(slug string) KeyOwner) *docker.Host {
	return docker.NewHost(e.hostDependencies(projectID, creds, keys))
}

// NeedCredentials are the generated need credentials of the owner a scope
// names (NeedSecretsScope, PreviewNeedScope), from this AppTown's store.
func (e *Environment) NeedCredentials(scope string) dependency.Credentials {
	var vault *secret.Vault
	if e.secrets != nil {
		vault = e.secrets.Vault
	}
	return NeedCredentials(vault, scope)
}

func (e *Environment) hostDependencies(projectID ids.ID, creds dependency.Credentials, keys func(slug string) KeyOwner) docker.Dependencies {
	return docker.Dependencies{
		Secrets: hostSecrets{secrets: e.secrets, project: projectID, env: e, keys: keys}, Needs: dependency.LocalRuntime{Catalog: e.needs, Credentials: creds}, Registry: hostRegistry{e.integrations, projectID}, Integrations: hostContributions{e.integrations, projectID},
		Routes: hostRoutes{e}, Probe: e.probe, ChownImage: e.chownImage,
	}
}

// PrepareBoot resolves everything a boot of c from ws reads, as the
// deployment named, with the project's registry credential: the one
// prepared value its boot uses, wherever it runs (design/operations/workers.md,
// "Previews on the worker"), with the needs' generated credentials creds
// gives. Preparing makes no key: WithRegistered does.
func (e *Environment) PrepareBoot(ctx context.Context, projectID ids.ID, slug, deployment, ws string, c *appcontract.Contract, creds dependency.Credentials) (docker.Prepared, error) {
	var login *docker.RegistryCredential
	if ri, err := e.integrations.Registry(ctx, projectID); err != nil {
		return docker.Prepared{}, &docker.BootError{Phase: docker.PhaseResolve, Err: fmt.Errorf("the project's registry credential: %w", err)}
	} else if ri != nil {
		login = &docker.RegistryCredential{Host: ri.Host, Username: ri.Username, Token: ri.Token}
	}
	// The boot runs c under the deployment's profile (a preview: the
	// preview rule); the prepared contract is what the boot runs.
	c, applied, err := e.Profiled(ctx, projectID, strs.FirstNonBlank(deployment, "preview"), ws, c)
	if err != nil {
		return docker.Prepared{}, &docker.BootError{Phase: docker.PhaseConfig, Err: err}
	}
	if why, err := e.refusesNeeds(ctx, c); err != nil || why != "" {
		if err == nil {
			err = fault.New(fault.Unprocessable, why)
		}
		return docker.Prepared{}, &docker.BootError{Phase: docker.PhaseResolve, Err: err}
	}
	c, _, err = e.DefaultMemory(ctx, projectID, c, applied)
	if err != nil {
		return docker.Prepared{}, err
	}
	p, err := docker.PrepareBoot(ctx, e.hostDependencies(projectID, creds, nil), login, slug, deployment, ws, c)
	if err != nil {
		return p, err
	}
	// Not a BootError: a store that cannot give the key is AppTown's
	// failure, not the app's.
	if p.SecretsFingerprint, err = e.secretsFingerprint(ctx, p.Secrets, p.Env); err != nil {
		return docker.Prepared{}, fmt.Errorf("identifying the app's secrets: %w", err)
	}
	return p, nil
}

// secretsFingerprint identifies the secret values a boot delivers per
// consumer and the integrations' environment, which may hold secrets and has
// no version, without revealing them: an HMAC of both, canonically encoded,
// under a key derived from the built-in store's (secret.Vault.Derived). The
// same values give the same fingerprint at every preparation, and a reader
// of the fingerprint cannot test guesses of the values against it. Empty
// when there is nothing to identify, and without a store, which only tests
// run without.
func (e *Environment) secretsFingerprint(ctx context.Context, secrets map[string]map[string]string, env []string) (string, error) {
	empty := len(env) == 0
	for _, values := range secrets {
		empty = empty && len(values) == 0
	}
	if empty || e.secrets == nil || e.secrets.Vault == nil {
		return "", nil
	}
	key, err := e.secrets.Vault.Derived(ctx, "preview-identity", "secret-values")
	if err != nil {
		return "", err
	}
	// Maps marshal with sorted keys; the environment is sorted as a copy.
	body, err := json.Marshal(struct {
		Secrets map[string]map[string]string `json:"secrets"`
		Env     []string                     `json:"env"`
	}{secrets, slices.Sorted(slices.Values(env))})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// WithRegistered gives a prepared boot the values its registered needs
// inject, its AI models key among them, made for (or kept by) owner.
func (e *Environment) WithRegistered(ctx context.Context, p docker.Prepared, owner KeyOwner) (docker.Prepared, error) {
	env, err := e.registerNeeds(ctx, p.Contract, owner)
	if err != nil || len(env) == 0 {
		return p, err
	}
	d := withRegistered(docker.Delivery{Env: func(consumer string) map[string]string { return p.Secrets[consumer] }, Receipts: p.Receipts}, env, p.Contract)
	secrets := map[string]map[string]string{}
	for _, consumer := range consumersOf(p.Contract) {
		secrets[consumer] = d.EnvFor(consumer)
	}
	p.Secrets, p.Receipts = secrets, d.Receipts
	return p, nil
}

// PreparedHost boots from prepared values alone, with this machine's probe.
func (e *Environment) PreparedHost(p docker.Prepared) *docker.Host {
	return docker.NewHost(p.Dependencies(e.probe, "", e.chownImage))
}

// ResolveDockerStack prepares the declared stack for validation/rendering. It
// reads only the dependency catalog, never boots, resolves secrets or rereads a
// declaration. The selected contract stays with its caller's admitted operation.
// creds are the generated need credentials of the owner it renders for (a
// Portainer deployment's); a caller that only validates passes nil.
func (e *Environment) ResolveDockerStack(ctx context.Context, c *appcontract.Contract, services []deploy.ServiceSpec, creds dependency.Credentials) (docker.ResolvedStack, error) {
	if c == nil {
		return docker.ResolvedStack{}, fmt.Errorf("a selected contract is required")
	}
	var err error
	c, err = e.needs.ResolveSizes(ctx, c)
	if err != nil {
		return docker.ResolvedStack{}, err
	}
	return docker.NewHost(docker.Dependencies{Needs: dependency.LocalRuntime{Catalog: e.needs, Credentials: creds}}).ResolveStack(ctx, c, services)
}

// ReleaseRepository is the repository a project's releases are published
// to, with the credential that writes it: its container-registry
// attachment's, or its connector's from the secrets store. A credential
// named but not held is refused, never read as anonymous.
func (e *Environment) ReleaseRepository(ctx context.Context, projectID ids.ID) (oci.Repository, error) {
	if e.integrations == nil {
		return oci.Repository{}, ErrNoReleaseRegistry
	}
	purpose, err := e.Purpose(ctx, projectID)
	if err != nil {
		return oci.Repository{}, err
	}
	read := func(id ids.ID) (*integration.Registry, error) { return e.integrations.Registry(ctx, id) }
	if !purpose.Integrations() {
		// A project given no integrations (an eval copy) publishes to its
		// own repository with AppTown's credential, used for that
		// repository alone; EvalPublication admits eval copies only.
		read = func(id ids.ID) (*integration.Registry, error) { return e.integrations.EvalPublication(ctx, id) }
	}
	ri, err := read(projectID)
	if err != nil {
		return oci.Repository{}, refused("%v", err)
	}
	if ri == nil {
		return oci.Repository{}, ErrNoReleaseRegistry
	}
	return oci.Repository{Name: ri.Repo, Credential: oci.Credential{Username: ri.Username, Password: ri.Token}}, nil
}

// ReleaseLogin is the credential a project's images by digest are pulled
// with: its container-registry attachment's, or nil (anonymous) when none
// is attached. An attachment or credential that cannot be read is an error.
func (e *Environment) ReleaseLogin(ctx context.Context, projectID ids.ID) (*docker.RegistryCredential, error) {
	if e.integrations == nil {
		return nil, nil
	}
	ri, err := e.integrations.Registry(ctx, projectID)
	if err != nil || ri == nil {
		return nil, err
	}
	return &docker.RegistryCredential{Host: ri.Host, Username: ri.Username, Token: ri.Token}, nil
}

// BundleLogin is the credential b's images are pulled with: an eval copy's
// own repository's (copyReleaseLogin), else the project's registry's.
func (e *Environment) BundleLogin(ctx context.Context, projectID ids.ID, b *ReleaseBundle) (*docker.RegistryCredential, error) {
	if login, err := e.copyReleaseLogin(ctx, projectID, b); err != nil || login != nil {
		return login, err
	}
	return e.ReleaseLogin(ctx, projectID)
}

// copyReleaseLogin is how an eval copy's published images are pulled where
// it is deployed: with the credential of the copy's own repository, for
// images by digest from that repository only (refusesBundle holds every
// image of a digest bundle to its registry). Nil for any other project; a
// purpose or repository that cannot be read is an error.
func (e *Environment) copyReleaseLogin(ctx context.Context, projectID ids.ID, b *ReleaseBundle) (*docker.RegistryCredential, error) {
	if b == nil || !b.ByDigest() {
		return nil, nil
	}
	purpose, err := e.Purpose(ctx, projectID)
	if err != nil || purpose.Integrations() {
		return nil, err
	}
	repo, err := e.ReleaseRepository(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if repo.Name != b.BuiltFrom.Registry {
		return nil, nil
	}
	host, _, _ := strings.Cut(repo.Name, "/")
	return &docker.RegistryCredential{Host: host, Username: repo.Credential.Username, Token: repo.Credential.Password}, nil
}

// --- needs fulfilled by registering ----------------------------------------

// FulfilledByRegistering says whether a need is fulfilled by registering
// with a shared service (an AI models key) rather than by a target.
func (e *Environment) FulfilledByRegistering(ctx context.Context, need string) (bool, error) {
	k, err := e.registerKind(ctx, need)
	return k != nil, err
}

// registerKind is the kind of need when it is fulfilled by registering, or nil.
func (e *Environment) registerKind(ctx context.Context, need string) (*dependency.Kind, error) {
	if e.needs == nil {
		return nil, nil
	}
	k, err := e.needs.ByCapability(ctx, need)
	if errors.Is(err, dependency.ErrUnknown) || errors.Is(err, dependency.ErrSpec) {
		return nil, nil // the target's own path reports an unknown need
	}
	if err != nil {
		return nil, fmt.Errorf("reading the dependency kind %s: %w", need, err)
	}
	if k.Register == nil || k.Local.Image != "" {
		return nil, nil
	}
	return k, nil
}

// registeredNeeds is c's needs the register step fulfils at boot. A need
// whose adapter AppTown does not run refuses.
func (e *Environment) registeredNeeds(ctx context.Context, c *appcontract.Contract) ([]registeredNeed, error) {
	var out []registeredNeed
	if c == nil {
		return nil, nil
	}
	for _, need := range c.Needs {
		k, err := e.registerKind(ctx, need)
		if err != nil {
			return nil, err
		}
		if k == nil || k.Integration {
			continue
		}
		if k.Register.Adapter != integration.AdapterOpenRouterKey {
			return nil, fault.Wrap(fault.Unprocessable, fmt.Errorf("this app needs %s, which is fulfilled by registering through %q, and AppTown cannot do that yet", need, k.Register.Adapter))
		}
		out = append(out, registeredNeed{name: need, kind: k})
	}
	return out, nil
}

// targetContract is c without the needs fulfilled by registering: what a
// target itself is asked to provide. Nil and need-less contracts are c.
func (e *Environment) targetContract(ctx context.Context, c *appcontract.Contract) (*appcontract.Contract, error) {
	if c == nil || len(c.Needs) == 0 {
		return c, nil
	}
	var keep []string
	for _, need := range c.Needs {
		k, err := e.registerKind(ctx, need)
		if err != nil {
			return nil, err
		}
		if k == nil {
			keep = append(keep, need)
		}
	}
	if len(keep) == len(c.Needs) {
		return c, nil
	}
	cc := *c
	cc.Needs = keep
	return &cc, nil
}

// RegisteredKeys is where the keys a boot's registered needs make are
// recorded: a deployment's owned list (Deployment's recorder), or a
// preview's (Preview's). Held is what the owner holds now; Record and
// Forget are called as OpenRouter confirms a key and as one is replaced.
type RegisteredKeys interface {
	Held(ctx context.Context) ([]integration.HeldKey, error)
	integration.KeyRecorder
}

// KeyOwner is what an AI models key is made for: a deployment or a preview.
type KeyOwner struct {
	ProjectID ids.ID
	Name      string // the key's name at OpenRouter
	Scope     string // where its value is kept
	Preview   bool
	Keys      RegisteredKeys
}

var injectRef = regexp.MustCompile(`\$\{([a-z_]+)\}`)

// registerNeeds runs the register step for c's needs on behalf of o and
// returns the variables the needs inject, rendered from each kind's inject
// templates. Nothing for an app that has no such need.
func (e *Environment) registerNeeds(ctx context.Context, c *appcontract.Contract, o KeyOwner) (map[string]string, error) {
	needs, err := e.registrableNeeds(ctx, c)
	if err != nil || len(needs) == 0 {
		return nil, err
	}
	env := map[string]string{}
	for _, n := range needs {
		held, err := o.Keys.Held(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading the keys %s holds: %w", o.Name, err)
		}
		req := integration.KeyRequest{ProjectID: o.ProjectID, Name: o.Name, Scope: o.Scope, Preview: o.Preview, Params: c.NeedParams[n.name]}
		if len(held) > 0 {
			req.Held = &held[0]
		}
		values, err := e.integrations.RegisterLLMKey(ctx, req, o.Keys)
		if err != nil {
			return nil, err
		}
		for name, tmpl := range n.kind.Register.Inject {
			var missing string
			v := injectRef.ReplaceAllStringFunc(tmpl, func(ref string) string {
				key := injectRef.FindStringSubmatch(ref)[1]
				out, ok := values[key]
				if !ok {
					missing = key
				}
				return out
			})
			if missing != "" {
				return nil, fmt.Errorf("the %s need injects ${%s}, which registering does not give", n.name, missing)
			}
			env[name] = v
		}
	}
	return env, nil
}

// registrableNeeds are c's needs fulfilled by registering, when this AppTown
// can register them; registerNeeds' refusal otherwise, before any key is
// made.
func (e *Environment) registrableNeeds(ctx context.Context, c *appcontract.Contract) ([]registeredNeed, error) {
	needs, err := e.registeredNeeds(ctx, c)
	if err != nil || len(needs) == 0 {
		return nil, err
	}
	if e.integrations == nil {
		return nil, fault.New(fault.Unprocessable, "this app needs AI models, and this AppTown has no integrations to make a key with")
	}
	return needs, nil
}

// registrable is registrableNeeds' judgement alone (Preflight).
func (e *Environment) registrable(ctx context.Context, c *appcontract.Contract) error {
	_, err := e.registrableNeeds(ctx, c)
	return err
}

// ChownImage is the image that gives a data volume to its app's user.
func (e *Environment) ChownImage() string { return e.chownImage }

// SealedHost is the built-in host's dependencies for a sealed boot of one
// project: its dependency catalog, registry login, probe and chown image,
// with the secrets and routes given and no integration's contributions (a
// rehearsal's withheld secrets and its one origin), its needs given creds.
func (e *Environment) SealedHost(projectID ids.ID, secrets docker.SecretResolver, routes docker.Routes, creds dependency.Credentials) docker.Dependencies {
	return docker.Dependencies{Secrets: secrets, Needs: dependency.LocalRuntime{Catalog: e.needs, Credentials: creds}, Registry: hostRegistry{e.integrations, projectID},
		Routes: routes, Probe: e.probe, ChownImage: e.chownImage}
}

// RefusesContract is the target's refusal of c, judged on what the target
// itself provides. A static-only bucket judges the whole contract: it has
// no server to hold a key.
func (e *Environment) RefusesContract(ctx context.Context, t *Target, c *appcontract.Contract) (string, error) {
	if why, err := e.refusesNeeds(ctx, c); err != nil || why != "" {
		return why, err
	}
	if t.Kind == KindS3Bucket {
		return t.RefusesContract(c), nil
	}
	tc, err := e.targetContract(ctx, c)
	if err != nil {
		return "", err
	}
	return t.RefusesContract(tc), nil
}

// refusesNeeds is the catalogue's refusal of c's needs as declared, the
// same on every target and for a preview (dependency.RefusesNeeds).
func (e *Environment) refusesNeeds(ctx context.Context, c *appcontract.Contract) (string, error) {
	if e.needs == nil || c == nil || len(c.Needs) == 0 {
		return "", nil
	}
	if why, err := e.needs.RefusesNeeds(ctx, c.Needs, c.NeedParams); err != nil || why != "" {
		return why, err
	}
	return e.needs.RefusesNeedRefs(ctx, c.NeedRefs())
}

// RefusesBrowserValues is the sentence for a static site whose page the
// project's integrations give values, bound for a target kind that cannot
// deliver them, or "". Every static image serves /_apptown/env.js, but only a
// Docker host and a Portainer stack give it the values; anywhere else the
// page would silently be given nothing, which breaks the integration's
// absence contract.
func (e *Environment) RefusesBrowserValues(ctx context.Context, kind string, projectID ids.ID, c *appcontract.Contract) (string, error) {
	// A bucket publishes the values as /_apptown/env.js with each release.
	if kind == KindDockerHost || kind == KindPortainer || kind == KindS3Bucket || e.integrations == nil {
		return "", nil
	}
	static := false
	for _, p := range c.Parts {
		static = static || p.IsStatic()
	}
	if !static {
		return "", nil
	}
	values, err := e.integrations.BrowserEnvironment(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("reading what the integrations give the page: %w", err)
	}
	if len(values) == 0 {
		return "", nil
	}
	return fmt.Sprintf("the attached integrations give this static site's page %s, and a %s target cannot deliver browser values yet; deploy it to a Docker host", strings.Join(slices.Sorted(maps.Keys(values)), ", "), kind), nil
}

// --- the host's collaborators -----------------------------------------------

type hostSecrets struct {
	secrets *secret.Service
	project ids.ID
	env     *Environment
	keys    func(slug string) KeyOwner
}

// Resolve is the app's secrets for a boot and, when the host was built for
// an owner, the values its registered needs inject: the key is made (or
// kept) here, as the boot reads its values.
func (s hostSecrets) Resolve(ctx context.Context, slug, deployment, ws string, c *appcontract.Contract) (docker.Delivery, error) {
	d, err := s.secrets.Resolve(ctx, s.project, slug, deployment, ws, c)
	if err != nil {
		return docker.Delivery{}, err
	}
	if s.keys == nil {
		return d.Docker(), nil
	}
	env, err := s.env.registerNeeds(ctx, c, s.keys(slug))
	if err != nil {
		return docker.Delivery{}, err
	}
	return withRegistered(d.Docker(), env, c), nil
}

type hostRegistry struct {
	integrations *integration.Service
	project      ids.ID
}

func (r hostRegistry) Login(ctx context.Context, dk docker.Scope, image string) error {
	ri, err := r.integrations.Registry(ctx, r.project)
	if err != nil {
		return fmt.Errorf("the project's registry credential: %w", err)
	}
	if ri == nil {
		return nil
	}
	return docker.LoginRegistry(ctx, dk, ri.Host, ri.Username, ri.Token, image)
}

type hostContributions struct {
	integrations *integration.Service
	project      ids.ID
}

func (c hostContributions) EnvFor(ctx context.Context) ([]string, error) {
	return c.integrations.Environment(ctx, c.project)
}

// BrowserEnv is the browser-safe values as the JSON a static part serves.
func (c hostContributions) BrowserEnv(ctx context.Context) (string, error) {
	values, err := c.integrations.BrowserEnvironment(ctx, c.project)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(values)
	return string(raw), err
}

type hostRoutes struct{ env *Environment }

func (r hostRoutes) FrontDoorHeaders() map[string]string      { return r.env.FrontDoorHeaders() }
func (r hostRoutes) PublicOrigins(ctx context.Context) string { return r.env.PublicOrigins(ctx) }

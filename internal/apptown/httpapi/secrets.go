package httpapi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

type Secrets struct {
	secrets      *secret.Service
	integrations *integration.Service
	projects     *project.Projects
	deployments  DeploymentLister
	db           *database.DB
	dataDir      string
	now          func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewSecrets(secrets *secret.Service, integrations *integration.Service, projects *project.Projects, deployments DeploymentLister, db *database.DB, dataDir string, now func() time.Time) Secrets {
	return Secrets{secrets: secrets, integrations: integrations, projects: projects, deployments: deployments, db: db, dataDir: dataDir, now: now}
}

// DeploymentLister names a project's deployments: the deployment owner's
// listing.
type DeploymentLister interface {
	List(ctx context.Context, projectID ids.ID) ([]deployment.Summary, error)
}

// The Settings → Secrets surface: what the app
// declares, which deployment uses which profile, and per deployment
// whether each secret is bound, where, at what version. Values never
// appear in a response, a log or an audit row.

// secretDeployments: the deployments a project can bind secrets for and
// choose a profile for. Bindings are keyed by deployment NAME
// (deployment.md §2.3): the previews, main, every standing deployment the
// project has, and the intranet tier when a release target is configured.
// A listing that cannot be read is an error, never a shorter list.
func (h Secrets) secretDeployments(ctx context.Context, projectID ids.ID) ([]string, error) {
	out := []string{"preview", "main"}
	seen := map[string]bool{"preview": true, "main": true}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if h.deployments != nil {
		listed, err := h.deployments.List(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("reading the project's deployments: %w", err)
		}
		for _, d := range listed {
			if d.Kind == deployment.Standing {
				add(d.Name)
			}
		}
	}
	t, err := h.integrations.PortainerTarget(ctx)
	if err != nil && !errors.Is(err, integration.ErrIntranetNotWired) {
		return nil, fmt.Errorf("reading the intranet's settings: %w", err)
	}
	if t != nil {
		add(t.ReleaseDeployment)
	}
	return out, nil
}

// The secrets routes' responses. Values appear only in SecretRevealed.
type (
	// ProjectSecrets is a project's secret declaration and, per deployment,
	// each secret's binding status. The declaration fields appear only once
	// the app declares its secrets.
	ProjectSecrets struct {
		Declared bool   `json:"declared"`
		Spec     string `json:"spec"`
		Problem  string `json:"problem,omitempty"`
		*SecretDeclaration
		Stores      []secret.StoreInfo `json:"stores"`
		Deployments []SecretDeployment `json:"deployments"`
		// Choosable is every profile a deployment may be given: default,
		// and every name app.yaml or secretspec.toml declares at main.
		Choosable []string `json:"choosable"`
	}
	SecretDeclaration struct {
		Project  string           `json:"project"`
		Revision string           `json:"revision"`
		Profiles []string         `json:"profiles"`
		Ignored  []string         `json:"ignored"`
		Secrets  []DeclaredSecret `json:"secrets"`
	}
	// DeclaredSecret is one declared secret across the app's profiles.
	DeclaredSecret struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Generate    bool            `json:"generate"`
		Domain      string          `json:"domain,omitempty"`
		Consumers   []string        `json:"consumers"`
		AllParts    bool            `json:"allParts"`
		Profiles    map[string]bool `json:"profiles"` // profile → required
	}
	// SecretDeployment is one deployment's profile and binding statuses.
	// Chosen is the deployment's profile, for both files; Profile is the
	// SecretSpec profile that applies it (its default when the file does
	// not declare it).
	SecretDeployment struct {
		Name    string         `json:"name"`
		Chosen  string         `json:"chosen"`
		Profile string         `json:"profile"`
		Secrets []SecretStatus `json:"secrets"`
		Missing []string       `json:"missing"`
	}
	SecretStatus struct {
		Name      string `json:"name"`
		Required  bool   `json:"required"`
		Bound     bool   `json:"bound"`
		Store     string `json:"store,omitempty"`
		Ref       string `json:"ref,omitempty"`
		Version   string `json:"version,omitempty"`
		Generated bool   `json:"generated,omitempty"`
		Default   bool   `json:"default,omitempty"` // unbound but the declaration carries a default
		UpdatedAt string `json:"updatedAt,omitempty"`
		UpdatedBy string `json:"updatedBy,omitempty"`
	}
	SecretProfileSet struct {
		Deployment string `json:"deployment"`
		Profile    string `json:"profile"`
	}
	SecretBound struct {
		Deployment string `json:"deployment"`
		Name       string `json:"name"`
		Store      string `json:"store"`
		Version    string `json:"version"`
		Bound      bool   `json:"bound"`
	}
	SecretRevealed struct {
		Name       string `json:"name"`
		Deployment string `json:"deployment"`
		Version    int    `json:"version"`
		Value      string `json:"value"`
	}
)

// nonNilStrings keeps a list a list in JSON: nil encodes null.
func nonNilStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// handleProjectSecrets: GET /api/v1/projects/{slug}/secrets
func (h Secrets) ProjectSecrets(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	ws, err := p.Repository(h.dataDir)
	if err != nil {
		WriteFault(w, err)
		return
	}
	stores, err := h.secrets.Stores()
	if err != nil {
		WriteFaultOr(w, err, http.StatusServiceUnavailable)
		return
	}
	out := ProjectSecrets{Declared: false, Spec: secret.SpecFile, Stores: stores, Deployments: []SecretDeployment{}}
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil && err != docker.ErrNoContract {
		out.Problem = err.Error()
		WriteJSON(w, 200, out)
		return
	}
	specPath := ""
	if c != nil {
		specPath = c.SecretSpecPath
	}
	spec, present, err := secret.ReadSpec(ws, specPath)
	if err != nil {
		out.Problem = err.Error()
		WriteJSON(w, 200, out)
		return
	}
	if !present {
		spec = nil
	}
	out.Choosable = slices.Sorted(maps.Keys(secret.DeclaredProfiles(spec, c)))
	names, lerr := h.secretDeployments(r.Context(), p.ID)
	if lerr != nil {
		WriteFault(w, lerr)
		return
	}
	if !present {
		// No secrets declared: each deployment's profile, for app.yaml.
		for _, env := range names {
			chosen, _, err := h.secrets.DeploymentProfile(r.Context(), p.ID, env, nil, c)
			if err != nil {
				WriteFault(w, err)
				return
			}
			out.Deployments = append(out.Deployments, SecretDeployment{Name: env, Chosen: chosen, Secrets: []SecretStatus{}, Missing: []string{}})
		}
		WriteJSON(w, 200, out)
		return
	}
	if c != nil {
		if err := spec.CheckScopesAgainst(c); err != nil {
			out.Problem = err.Error()
		}
	}
	out.Declared = true
	out.SecretDeclaration = &SecretDeclaration{Project: spec.Name, Revision: spec.Revision,
		Profiles: nonNilStrings(spec.ProfileNames()), Ignored: nonNilStrings(spec.Ignored)}

	// The declared secrets, across profiles.
	byName := map[string]*DeclaredSecret{}
	for _, pname := range spec.ProfileNames() {
		for _, s := range spec.SecretsIn(pname) {
			d := byName[s.Name]
			if d == nil {
				consumers, all := spec.ConsumersOf(s.Name)
				if consumers == nil {
					consumers = []string{}
				}
				d = &DeclaredSecret{Name: s.Name, Description: s.Description, Generate: s.Generate,
					Consumers: consumers, AllParts: all, Profiles: map[string]bool{}}
				if c != nil {
					d.Domain = c.SecretGenerate[s.Name]
				}
				if d.Generate && d.Domain == "" {
					d.Domain = "deployment"
				}
				byName[s.Name] = d
			}
			d.Profiles[pname] = s.Required
		}
	}
	secretNames := make([]string, 0, len(byName))
	for n := range byName {
		secretNames = append(secretNames, n)
	}
	sort.Strings(secretNames)
	secrets := make([]DeclaredSecret, 0, len(secretNames))
	for _, n := range secretNames {
		secrets = append(secrets, *byName[n])
	}
	out.Secrets = secrets

	// Per deployment: the profile in use and each secret's binding status.
	envs := []SecretDeployment{}
	for _, env := range names {
		chosen, _, err := h.secrets.DeploymentProfile(r.Context(), p.ID, env, spec, c)
		if err != nil {
			WriteFault(w, err)
			return
		}
		profile := secret.SpecProfile(spec, chosen)
		bindings, err := h.secrets.Bindings.For(r.Context(), p.ID, env)
		if err != nil {
			WriteFaultOr(w, err, http.StatusServiceUnavailable)
			return
		}
		ev := SecretDeployment{Name: env, Chosen: chosen, Profile: profile, Secrets: []SecretStatus{}, Missing: []string{}}
		for _, s := range spec.SecretsIn(profile) {
			st := SecretStatus{Name: s.Name, Required: s.Required}
			if b, ok := bindings[s.Name]; ok {
				st.Bound, st.Store, st.Version, st.Generated, st.UpdatedAt, st.UpdatedBy = true, b.Store, b.Version, b.Generated, b.UpdatedAt, b.UpdatedBy
				if b.Store == secret.StoreInfisical {
					st.Ref = b.Ref
				}
			} else if s.Default != "" {
				st.Default = true
			} else if s.Generate {
				st.Generated = true // will be minted on first boot
			} else if s.Required {
				ev.Missing = append(ev.Missing, s.Name)
			}
			ev.Secrets = append(ev.Secrets, st)
		}
		envs = append(envs, ev)
	}
	out.Deployments = envs
	WriteJSON(w, 200, out)
}

// handleSetSecretProfile: PUT /api/v1/projects/{slug}/secrets/{deployment} {profile}
func (h Secrets) SetSecretProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	env := r.PathValue("deployment")
	if known, err := h.knownSecretDeployment(r.Context(), p.ID, env); err != nil {
		WriteFault(w, err)
		return
	} else if !known {
		WriteError(w, 404, "no deployment %q", env)
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "profile required"}, func(in struct {
		Profile string `json:"profile"`
	}) {
		if in.Profile == "" {
			WriteError(w, 400, "profile required")
			return
		}
		// The deployment's one profile, for app.yaml and secretspec.toml
		// alike: default (the base) or a name either file declares at main.
		ws, err := p.Repository(h.dataDir)
		if err != nil {
			WriteFault(w, err)
			return
		}
		c, err := docker.ReadWorkspaceContract(ws)
		if err != nil && err != docker.ErrNoContract {
			WriteError(w, 409, "the app's contract cannot be read: %v", err)
			return
		}
		specPath := ""
		if c != nil {
			specPath = c.SecretSpecPath
		}
		spec, present, err := secret.ReadSpec(ws, specPath)
		if err != nil {
			WriteError(w, 409, "the app's secret declaration cannot be read: %v", err)
			return
		}
		if !present {
			spec = nil
		}
		declared := secret.DeclaredProfiles(spec, c)
		if !declared[in.Profile] {
			WriteError(w, 422, "the app declares no profile %q (it has %s)", in.Profile, strings.Join(slices.Sorted(maps.Keys(declared)), ", "))
			return
		}
		if err := h.secrets.Profiles.Set(r.Context(), p.ID, env, in.Profile, Actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, SecretProfileSet{Deployment: env, Profile: in.Profile})
	})
}

// handleBindSecret: PUT /api/v1/projects/{slug}/secrets/{deployment}/{name}
//
//	{"store":"internal","value":"..."}   put a value into the built-in store (write-only)
//	{"store":"infisical","ref":"KEY"}    point at a key in Infisical
//
// The value is obfuscated and stored; it is never echoed, logged or audited.
func (h Secrets) BindSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	env, name := r.PathValue("deployment"), r.PathValue("name")
	if known, err := h.knownSecretDeployment(r.Context(), p.ID, env); err != nil {
		WriteFault(w, err)
		return
	} else if !known {
		WriteError(w, 404, "no deployment %q", env)
		return
	}
	if !secret.ValidName(name) {
		WriteError(w, 400, "%q is not a secret name (A-Z, 0-9, _)", name)
		return
	}
	jsonCommand(w, r, jsonBody{limit: 1 << 20}, func(in struct {
		Store string `json:"store"`
		Value string `json:"value"`
		Ref   string `json:"ref"`
	}) {
		who := Actor(r)
		b := secret.Binding{Deployment: env, Name: name, UpdatedBy: who}
		switch in.Store {
		case "", secret.StoreInternal:
			if in.Value == "" {
				WriteError(w, 400, "a value is required for the built-in store")
				return
			}
			// The value, its binding and the receipt are one act.
			bound, err := h.secrets.BindStored(r.Context(), p.ID, p.Slug, env, name, in.Value, who)
			if err != nil {
				WriteError(w, 500, "storing the value: %v", err)
				return
			}
			WriteJSON(w, 200, SecretBound{Deployment: env, Name: name, Store: bound.Store, Version: bound.Version, Bound: true})
			return
		case secret.StoreInfisical:
			if in.Ref == "" {
				WriteError(w, 400, "the Infisical key name is required")
				return
			}
			values, err := h.secrets.InfisicalValues(r.Context())
			if err != nil {
				WriteError(w, 409, "%v", err)
				return
			}
			if v, ok := values[in.Ref]; !ok || v == "" {
				WriteError(w, 422, "Infisical has no key %s in the project and environment this AppTown reads", in.Ref)
				return
			}
			b.Store, b.Ref, b.Version = secret.StoreInfisical, in.Ref, "infisical"
		default:
			WriteError(w, 400, "unknown store %q (internal | infisical)", in.Store)
			return
		}
		if err := h.secrets.Bindings.Put(r.Context(), p.ID, b); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, SecretBound{Deployment: env, Name: name, Store: b.Store, Version: b.Version, Bound: true})
	})
}

// handleUnbindSecret: DELETE /api/v1/projects/{slug}/secrets/{deployment}/{name}.
// Removes the binding and unreferenced built-in versions with their receipt.
func (h Secrets) UnbindSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	env, name := r.PathValue("deployment"), r.PathValue("name")
	if err := h.secrets.Bindings.Unbind(r.Context(), p.ID, env, name, Actor(r)); err != nil {
		if errors.Is(err, secret.ErrBindingMissing) {
			WriteError(w, 404, "%s is not bound in %s", name, env)
		} else {
			WriteFault(w, err)
		}
		return
	}
	WriteJSON(w, 200, Done{OK: true})
}

func (h Secrets) knownSecretDeployment(ctx context.Context, projectID ids.ID, env string) (bool, error) {
	names, err := h.secretDeployments(ctx, projectID)
	return slices.Contains(names, env), err
}

// handleRevealSecret: GET /api/v1/projects/{slug}/secrets/{deployment}/{name}/reveal —
// the one way a person reads a value the built-in store holds (a generated
// Django superuser password, say). Administrators only, by capability, and
// every read is an audit receipt naming who, which secret, which deployment
// and which version — never the value. Values in Infisical are Infisical's
// to show.
func (h Secrets) RevealSecret(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	env, name := r.PathValue("deployment"), r.PathValue("name")
	got, err := h.secrets.Reveal(r.Context(), p.ID, env, name, Actor(r))
	switch {
	case errors.Is(err, secret.ErrBindingMissing):
		WriteError(w, 404, "%s is not bound in %s", name, env)
	case errors.Is(err, secret.ErrNotStored):
		WriteError(w, 404, "the built-in store holds no value for %s in %s", name, env)
	case err != nil:
		WriteFault(w, err)
	default:
		WriteJSON(w, 200, SecretRevealed{Name: name, Deployment: env, Version: got.Version, Value: got.Value})
	}
}

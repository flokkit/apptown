package appcontract

import (
	"maps"

	"github.com/flokkit/apptown/internal/trust"
)

// Contract is the canonical declaration parsed from .apptown/app.yaml. Callers
// retain it as source intent and use Prepare for mutable execution inputs.
type Contract struct {
	Needs      []string
	NeedParams map[string]map[string]string
	// NeedSizes are catalogue size selections; Command is filled only by execution resolution.
	NeedSizes map[string]NeedSize
	// Version is the contract's shape version: 1 (or none written) or 2
	// (parse.go, currentContractVersion).
	Version int
	// NeedResources is the most each need's service may use, by need;
	// a need that declares nothing is absent (no limit). It is not a
	// parameter: the catalogue never sees it.
	NeedResources map[string]Resources

	// Parts is the canonical declaration; exactly one is public.
	Parts []Part
	// Init are ordered run-to-completion containers (migrations, asset builds)
	// that must succeed BEFORE the app parts start. app.yaml-only.
	Init []InitContainer
	// Secrets metadata: the declaration file's path
	// (default secretspec.toml) and which generated secrets are stable per
	// project rather than per deployment. The declaration itself is read
	// from the tree at boot (secretspec.go); values never live here.
	SecretSpecPath string
	SecretGenerate map[string]string
	// RehearsalChecks are the app's declared data checks, each "GET <path>"
	// or "GET <path> expect <status>": paths it answers without its own
	// sign-in that read its database. A restore rehearsal asks them after
	// readiness and the front page (design/subsystems/deployment.md,
	// "Restore rehearsals").
	RehearsalChecks []string
	// Profiles are the declared profiles by name, each part's or need's
	// changes by its name (resources.go); a need's are its resources alone.
	// The base, default, is the declaration itself.
	Profiles map[string]map[string]ProfilePart
}

// Disposable says the app declared need's data not worth keeping
// (`redis: {disposable: true}`, a cache): it is never copied, starts empty,
// and reads as disposable rather than as a gap in its protection.
func (c *Contract) Disposable(need string) bool { return c.NeedParams[need]["disposable"] == "true" }

// InitContainer runs once to completion before the app parts (ECS: an
// essential:false container the app parts dependsOn with condition SUCCESS).
type InitContainer struct {
	Name    string
	Build   string
	Image   string
	Command []string
	Env     map[string]string
}

// Part is one app-owned container from .apptown/app.yaml's `app:` map. Compose
// conventions for the run mechanics; Public marks the front door (exactly one).
type Part struct {
	Name   string
	Build  string // build context (compose `build:`)
	Image  string // pushed ref (compose `image:`)
	Static string // client-only: serve this committed folder
	// StaticBuild is a static site built first: its output folder is
	// served as Static is. Exactly one of Build, Image, Static, StaticBuild.
	StaticBuild *StaticBuild
	Command     []string          // compose `command:`
	Port        int               // container port (from compose `ports:`)
	Env         map[string]string // compose `environment:`
	DataDir     string            // AppTown: a named volume that survives redeploys
	Health      string            // LEGACY healthcheck shell command; exit 0 = healthy
	Intent      *Health           // structured per-part health intent (design/subsystems/healthchecks.md); nil = none declared
	Public      bool              // the front door the preview/deploy URL points at
	// Resources is the most the part may use; zero fields are no limit.
	Resources Resources
	// ServesStatic marks a static part after its files are packed into the
	// file-server image (Static is cleared then): it is still a static site,
	// and AppTown still gives its page browser values.
	ServesStatic bool
}

// StaticBuild declares a static site AppTown builds before serving it:
// the Dockerfile in Context is built to its Target stage (the last when
// empty), and the folder Output inside that stage's files is the site.
type StaticBuild struct {
	Context, Target, Output string
}

// IsStatic says whether the part is a static site, served by AppTown's
// file server: a committed folder, a declared build, or an image AppTown
// already packed from either. It classifies; it does not select a source
// (an image already packed is never a folder to mount or rebuild).
func (p *Part) IsStatic() bool {
	return p != nil && (p.Static != "" || p.StaticBuild != nil || p.ServesStatic)
}

// BrowserEnvVar carries a static part's browser-safe values. AppTown alone
// sets it; a contract or secret that names it is refused, so nothing can
// replace what the page is given.
const BrowserEnvVar = "APPTOWN_BROWSER_ENV"

// ReleaseEnvVar carries a static part's release identity
// (ReleaseIdentity); its file server answers it at ReleasePath, so a target
// whose front door AppTown can only reach from outside (App Platform) can
// witness that the part it deployed, in this release, is what answers. The
// AppTown sets it.
const ReleaseEnvVar = "APPTOWN_RELEASE"

// TrustEnvVar carries the extra certificate authorities an app's own
// containers trust, as the script that installs them (internal/trust),
// which the app's image runs at start. AppTown sets it.
const TrustEnvVar = trust.EnvVar

// AppTownSetEnv are the environment names AppTown alone sets on a part. An
// app that set one would win over AppTown's value on one target and lose on
// another, so the contract refuses it in a part's or a profile's
// environment (the contract reference, "The environment an app gets").
var AppTownSetEnv = []string{"PORT", "APPTOWN_ORIGINS", "DATA_DIR", ReleaseEnvVar, BrowserEnvVar, TrustEnvVar}

// setsAppTownEnv names the first of AppTownSetEnv that env sets, or "".
func setsAppTownEnv(env environment) string {
	for _, name := range AppTownSetEnv {
		if _, ok := env[name]; ok {
			return name
		}
	}
	return ""
}

// ReleasePath is where a static site's file server answers ReleaseEnvVar.
const ReleasePath = "/_apptown/release"

// ReleaseIdentity is what a static part's file server is told to answer:
// the part by name and the revision its release was cut from.
func ReleaseIdentity(part, revision string) string { return part + "@" + revision }

// PublicRelease is the release identity c's front door answers when it is
// served from revision: the public part's, when that part is a static site
// (its file server answers ReleasePath), and "" otherwise, or when there is
// no revision, whose release is not witnessed. Every target that runs a
// release sets and witnesses this one value.
func (c *Contract) PublicRelease(revision string) string {
	if c == nil || revision == "" {
		return ""
	}
	pub := c.PublicPart()
	if !pub.IsStatic() {
		return ""
	}
	return ReleaseIdentity(pub.Name, revision)
}

// Plan holds one execution's private inputs. Runtime owners select images and
// apply scoped secrets to this plan without changing the source declaration.
type Plan struct {
	Parts []Part
	Init  []InitContainer
}

// PublicPart returns the declared front door, or nil if none is present.
// The returned part belongs to the declaration; use Prepare before modifying it.
func (c *Contract) PublicPart() *Part { return findPublicPart(c.Parts) }

// PublicOnly is the plan with its public part and init steps alone: what a
// restore rehearsal runs, so workers and schedulers stay off.
func (p *Plan) PublicOnly() *Plan {
	out := &Plan{Init: p.Init}
	if pub := p.PublicPart(); pub != nil {
		out.Parts = []Part{*pub}
	}
	return out
}

// PublicPart returns the plan's mutable front door, or nil if none is present.
func (p *Plan) PublicPart() *Part { return findPublicPart(p.Parts) }
func findPublicPart(parts []Part) *Part {
	for i := range parts {
		if parts[i].Public {
			return &parts[i]
		}
	}
	return nil
}

// Prepare copies a declaration into an independent execution plan. Resolved
// service connections override each consumer's own environment; public-only
// values never flow into workers or init containers. It performs no I/O.
func Prepare(c *Contract, connections map[string]string) *Plan {
	p := &Plan{Parts: append([]Part(nil), c.Parts...), Init: append([]InitContainer(nil), c.Init...)}
	for i := range p.Parts {
		part := &p.Parts[i]
		part.Command = append([]string(nil), part.Command...)
		if part.Intent != nil {
			health := *part.Intent
			health.Readiness = copyHealthProbe(health.Readiness)
			health.Liveness = copyHealthProbe(health.Liveness)
			part.Intent = &health
		}
		part.Env = MergeEnv(part.Env, connections)
	}
	for i := range p.Init {
		init := &p.Init[i]
		init.Command = append([]string(nil), init.Command...)
		init.Env = MergeEnv(init.Env, connections)
	}
	return p
}

// PrepareWired is Prepare after each part's and init step's need references
// are replaced with the values connections gives (WireEnv), the path every
// container target takes so wiring is the same everywhere.
func PrepareWired(c *Contract, connections map[string]string) (*Plan, error) {
	p := Prepare(c, nil)
	for i := range p.Parts {
		env, err := WireEnv(c.Version, "app part "+p.Parts[i].Name, p.Parts[i].Env, connections)
		if err != nil {
			return nil, err
		}
		p.Parts[i].Env = MergeEnv(env, connections)
	}
	for i := range p.Init {
		env, err := WireEnv(c.Version, "init step "+p.Init[i].Name, p.Init[i].Env, connections)
		if err != nil {
			return nil, err
		}
		p.Init[i].Env = MergeEnv(env, connections)
	}
	return p, nil
}

func copyHealthProbe(p *Probe) *Probe {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Exec = append([]any(nil), p.Exec...)
	return &copy
}

// MergeEnv is a new environment: base, with override's values taking
// precedence. Neither input is changed, so a part's declared environment
// stays as written.
func MergeEnv(base, override map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(override))
	maps.Copy(out, base)
	maps.Copy(out, override)
	return out
}

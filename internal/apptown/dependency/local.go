package dependency

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
)

// LocalService is one resolved emulator and the environment its consumers need.
type LocalService struct {
	Name, Image, Health string
	// Data is where the emulator keeps its data (the kind's local data path).
	Data                string
	Port                int
	Command, Entrypoint []string
	ConfigAtStart       bool
	KeepsImage          bool
	Env, Inject         map[string]string
	// StepEnv is what a step's server adds to Env (ForSteps).
	StepEnv map[string]string
	// Credentials are the owner's generated values in Env and Inject
	// (deploy.ServiceSpec.Credentials), sorted; none for the catalogue's.
	Credentials []string
}

// ForSteps is the service as AppTown starts it beside its own steps and
// for apptown dev, where the app's own tests run: its Env with the kind's
// StepEnv over it.
func (s LocalService) ForSteps() LocalService {
	if len(s.StepEnv) == 0 {
		return s
	}
	env := maps.Clone(s.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, s.StepEnv)
	s.Env = env
	return s
}

// Spec is the service to start for this emulator, with where it keeps its
// data, so a throwaway start can give it a disposable folder there.
func (s LocalService) Spec() deploy.ServiceSpec {
	return deploy.ServiceSpec{Name: s.Name, Image: s.Image, Env: s.Env, Command: s.Command, Entrypoint: s.Entrypoint, ConfigAtStart: s.ConfigAtStart, KeepsImage: s.KeepsImage, Health: s.Health, Data: s.Data, Credentials: s.Credentials}
}

type localPlan struct {
	Services []LocalService
	Env      map[string]string
	Unknown  []string
}

// Credentials gives one owner's value of a kind's generated credential
// (Kind.Generate) by the kind's capability and the credential's name: a
// deployment's, a preview's, or one operation's. Nil gives the catalogue's
// own values, which only AppTown's throwaway servers use (its steps,
// apptown dev, a check's environment).
type Credentials func(ctx context.Context, capability, name string) (string, error)

// Generated is the kind's generated credentials as creds gives them, by
// name; nil when creds is nil or the kind generates none.
func (k *Kind) Generated(ctx context.Context, creds Credentials) (map[string]string, error) {
	if creds == nil || len(k.Generate) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(k.Generate))
	for _, name := range k.Generate {
		v, err := creds(ctx, k.Capability, name)
		if err != nil {
			return nil, fmt.Errorf("the %s need's %s: %w", k.Capability, name, err)
		}
		if v == "" {
			return nil, fmt.Errorf("the %s need's %s has no value", k.Capability, name)
		}
		out[name] = v
	}
	return out, nil
}

// ResolveLocal overlays declared parameters on defaults. An empty host uses the
// capability's network alias; process callers supply their reachable host.
// generated are the owner's generated credentials (Generated); nil uses the
// catalogue's values. Substitution is single-pass: parameter values are
// data, not more templates. A register-only kind has no local service.
func (k *Kind) ResolveLocal(params map[string]string, host string, generated map[string]string) *LocalService {
	if k.Local.Image == "" {
		return nil
	}
	vals := k.values(params, generated)
	if host == "" {
		host = k.Capability
	}
	vals["host"] = host
	pairs := make([]string, 0, 2*len(vals))
	for key, value := range vals {
		pairs = append(pairs, "{"+key+"}", value)
	}
	r := strings.NewReplacer(pairs...)
	render := func(in map[string]string) map[string]string {
		out := maps.Clone(in)
		for key, value := range out {
			out[key] = r.Replace(value)
		}
		return out
	}
	s := LocalService{Name: k.Capability, Image: r.Replace(k.Local.Image),
		Health: r.Replace(k.Local.Health), Data: k.Local.Data, Port: k.Local.Port,
		Env: render(k.Local.Env), Inject: render(k.Local.Inject), StepEnv: render(k.Local.StepEnv)}
	for _, arg := range k.Local.Command {
		s.Command = append(s.Command, r.Replace(arg))
	}
	for _, arg := range k.Local.Entrypoint {
		s.Entrypoint = append(s.Entrypoint, r.Replace(arg))
	}
	s.ConfigAtStart = k.Local.ConfigAtStart
	s.KeepsImage = k.Local.KeepsImage
	for _, name := range k.Generate {
		if v, ok := generated[name]; ok {
			s.Credentials = append(s.Credentials, v)
		}
	}
	slices.Sort(s.Credentials)
	return &s
}

// ResolveLocal preserves best-effort catalog resolution: absent or unreadable
// kinds are reported as unknown, with unexpected read failures logged. Callers
// starting services reject Unknown; environment-only callers may use partial Env.
// Register-only kinds are known but have no emulator or local environment.
// It gives the catalogue's own credentials: for AppTown's throwaway
// servers and apptown dev, never a deployment or a preview (LocalRuntime).
func (c *Catalog) ResolveLocal(ctx context.Context, needs []string, params map[string]map[string]string, host string) localPlan {
	out, _ := c.resolveLocal(ctx, needs, params, host, nil)
	return out
}

// resolveLocal is ResolveLocal with an owner's credentials; a credential
// that cannot be given is the error.
func (c *Catalog) resolveLocal(ctx context.Context, needs []string, params map[string]map[string]string, host string, creds Credentials) (localPlan, error) {
	out := localPlan{Env: map[string]string{}}
	for _, need := range needs {
		kind, err := c.ByCapability(ctx, need)
		if err != nil {
			if !errors.Is(err, ErrUnknown) && !errors.Is(err, ErrSpec) {
				log.Printf("reading the dependency kind %s: %v", need, err)
			}
			out.Unknown = append(out.Unknown, need)
			continue
		}
		if kind.Local.Image == "" {
			continue
		}
		generated, err := kind.Generated(ctx, creds)
		if err != nil {
			return localPlan{}, err
		}
		s := kind.ResolveLocal(params[need], host, generated)
		out.Services = append(out.Services, *s)
		maps.Copy(out.Env, s.Inject)
	}
	return out, nil
}

// LocalConnection is the connection to this kind's local emulator as its
// consumers reach it on the stack's network: its alias, its port, and the
// declared database, user and password over the defaults, and generated
// (Generated) over the catalogue's credentials. A copy's commands are given
// it to fill a new, empty emulator (a restore rehearsal), and to reach a
// running one, whose generated values are what its container was started
// with. version is the emulator's, for the copy's client image.
func (k *Kind) LocalConnection(params, generated map[string]string) (conn deploy.Connection, version string) {
	vals := k.values(params, generated)
	sslmode := vals["sslmode"]
	if sslmode == "" {
		sslmode = "disable" // an emulator speaks no TLS
	}
	return deploy.Connection{Host: k.Capability, Port: k.Local.Port, Database: vals["database"], User: vals["user"], Password: vals["password"], SSLMode: sslmode}, vals["version"]
}

// values are the kind's interpolation values: its defaults, the app's
// parameters over them, AppTown's credentials over both, and the
// owner's generated values of its generated credentials over those.
func (k *Kind) values(params, generated map[string]string) map[string]string {
	vals := map[string]string{}
	maps.Copy(vals, k.Defaults)
	maps.Copy(vals, params)
	maps.Copy(vals, k.Credentials)
	for _, name := range k.Generate {
		if v, ok := generated[name]; ok {
			vals[name] = v
		}
	}
	return vals
}

package secret

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/fault"
)

// SpecFile is the default application declaration filename. ParseSpec supports
// a subset of SecretSpec 1.0; ignored features are reported in Spec.Ignored.
const SpecFile = "secretspec.toml"
const specRevision = "1.0"

// secretSpecDoc mirrors the TOML shape. Fields AppTown ignores are still
// decoded so the declaration parses as SecretSpec would parse it.
type secretSpecDoc struct {
	Project struct {
		Name          string   `toml:"name"`
		Revision      string   `toml:"revision"`
		Extends       []string `toml:"extends"`
		RequireReason any      `toml:"require_reason"`
	} `toml:"project"`
	Profiles  map[string]map[string]secretSpecEntry `toml:"profiles"`
	Scopes    map[string]secretSpecScope            `toml:"scopes"`
	Providers map[string]any                        `toml:"providers"`
}

// secretSpecEntry is one secret in one profile. `required` may be a bool or
// a presence-group table in SecretSpec; AppTown reads the bool and treats
// a table as "required" (the safe reading) while reporting it as unsupported.
type secretSpecEntry struct {
	Description string   `toml:"description"`
	Required    any      `toml:"required"`
	Default     *string  `toml:"default"`
	Providers   []string `toml:"providers"`
	Ref         any      `toml:"ref"`
	AsPath      bool     `toml:"as_path"`
	Encoding    string   `toml:"encoding"`
	Composed    string   `toml:"composed"`
	Generate    any      `toml:"generate"`
	Prompt      bool     `toml:"prompt"`
	// Defaults is the per-profile `[profiles.X.defaults]` table, which
	// shares the map with secret entries in the TOML shape.
	Inherit *bool `toml:"inherit"`
}

type secretSpecScope struct {
	Secrets []string `toml:"secrets"`
}

// Spec is AppTown's reading of a declaration.
type Spec struct {
	Name     string
	Revision string
	// Profiles: profile name → secret name → entry, AFTER default-inheritance
	// (a non-default profile inherits every entry of `default` it does not
	// override, unless its defaults table says inherit = false).
	Profiles map[string]map[string]DeclaredSecret
	// Inherits says whether a profile other than default took default's
	// entries: false only where its defaults table says inherit = false.
	// A restore rehearsal's allow-list must not inherit (ResolveRehearsal).
	Inherits map[string]bool
	// Scopes: consumer name → secret names. A consumer is an app part by
	// name, or an init step as "init-<name>". Empty means every consumer
	// receives every secret.
	Scopes map[string][]string
	// Ignored names the declaration's features AppTown does not honour,
	// so the Settings page can say so instead of the app finding out.
	Ignored []string
}

type DeclaredSecret struct {
	Name        string
	Description string
	Required    bool
	Default     string
	// HasDefault is whether the entry declares a default at all, so an
	// empty one (`default = ""`) is a declared value, not an absent one.
	HasDefault bool
	Generate   bool
}

// ReadSpec reads the declaration from a workspace. present is false
// when the file does not exist — an app without secrets declares nothing.
func ReadSpec(ws, path string) (spec *Spec, present bool, err error) {
	if path == "" {
		path = SpecFile
	}
	if err := appcontract.CheckSpecPath(path); err != nil {
		return nil, true, fault.Wrap(fault.Invalid, fmt.Errorf("secrets: spec: %w", err))
	}
	raw, err := os.ReadFile(filepath.Join(ws, filepath.Clean(path)))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	spec, err = ParseSpec(raw)
	if err != nil {
		return nil, true, fault.Wrap(fault.Invalid, fmt.Errorf("%s: %w", path, err))
	}
	return spec, true, nil
}

func ParseSpec(raw []byte) (*Spec, error) {
	var doc secretSpecDoc
	md, err := toml.Decode(string(raw), &doc)
	if err != nil {
		return nil, err
	}
	if doc.Project.Revision != specRevision {
		return nil, fmt.Errorf("[project] revision must be %q (AppTown reads SecretSpec %s); got %q", specRevision, specRevision, doc.Project.Revision)
	}
	if doc.Project.Name == "" {
		return nil, fmt.Errorf("[project] name is required")
	}
	// A table AppTown does not read is a declaration that never takes
	// effect: a generate = true under [secrets.X] leaves its
	// [profiles.default.X] entry required-and-missing, so boot stops for a
	// value the author believed would be generated. Name the stray key and where the entry belongs.
	for _, k := range md.Undecoded() {
		key := k.String()
		if strings.HasPrefix(key, "project.") || strings.HasPrefix(key, "profiles.") || strings.HasPrefix(key, "scopes.") || strings.HasPrefix(key, "providers.") {
			continue // an unknown field inside a table AppTown reads is tolerated, as SecretSpec tolerates it
		}
		return nil, fmt.Errorf("%s is not SecretSpec 1.0: a secret is declared under [profiles.<name>] as NAME = { description = \"…\", required = true | generate = true }", key)
	}
	if len(doc.Profiles) == 0 {
		return nil, fmt.Errorf("at least one [profiles.<name>] table is required")
	}
	out := &Spec{Name: doc.Project.Name, Revision: doc.Project.Revision,
		Profiles: map[string]map[string]DeclaredSecret{}, Inherits: map[string]bool{}, Scopes: map[string][]string{}}
	if len(doc.Project.Extends) > 0 {
		out.Ignored = append(out.Ignored, "[project] extends (AppTown reads one file)")
	}
	if len(doc.Providers) > 0 {
		out.Ignored = append(out.Ignored, "[providers] (where values live is AppTown's binding, not the repo's)")
	}
	// Per profile: split the `defaults` pseudo-entry from real secrets.
	type rawProfile struct {
		entries map[string]secretSpecEntry
		inherit bool
	}
	profiles := map[string]rawProfile{}
	names := make([]string, 0, len(doc.Profiles))
	for pname, entries := range doc.Profiles {
		names = append(names, pname)
		rp := rawProfile{entries: map[string]secretSpecEntry{}, inherit: true}
		for sname, e := range entries {
			if sname == "defaults" {
				if e.Inherit != nil {
					rp.inherit = *e.Inherit
				}
				if len(e.Providers) > 0 {
					out.Ignored = append(out.Ignored, fmt.Sprintf("[profiles.%s.defaults] providers", pname))
				}
				continue
			}
			if !secretNameRe.MatchString(sname) {
				return nil, fmt.Errorf("[profiles.%s] %q is not an environment variable name (A-Z, 0-9, _)", pname, sname)
			}
			// A secret named like a variable AppTown sets would win on one
			// target and lose on another; APPTOWN_ names are kept for
			// variables AppTown adds later.
			if why := appcontract.ReservedEnvName(sname); why != "" {
				return nil, fmt.Errorf("[profiles.%s] %s is AppTown's (%s); name the secret something else", pname, sname, why)
			}
			rp.entries[sname] = e
		}
		profiles[pname] = rp
	}
	sort.Strings(names)
	convert := func(pname, sname string, e secretSpecEntry) DeclaredSecret {
		s := DeclaredSecret{Name: sname, Description: e.Description, Required: true}
		if e.Default != nil {
			s.Default, s.HasDefault = *e.Default, true
		}
		switch v := e.Required.(type) {
		case bool:
			s.Required = v
		case nil:
		default:
			out.Ignored = append(out.Ignored, fmt.Sprintf("[profiles.%s] %s required presence group (read as required)", pname, sname))
		}
		switch v := e.Generate.(type) {
		case bool:
			s.Generate = v
		case nil:
		default:
			s.Generate = true // a `generate = { type = ... }` table: AppTown generates a random value
		}
		if len(e.Providers) > 0 || e.Ref != nil {
			out.Ignored = append(out.Ignored, fmt.Sprintf("[profiles.%s] %s providers/ref (bound in AppTown instead)", pname, sname))
		}
		if e.AsPath {
			out.Ignored = append(out.Ignored, fmt.Sprintf("[profiles.%s] %s as_path (file delivery is not built yet; delivered as env)", pname, sname))
		}
		if e.Composed != "" || e.Prompt || e.Encoding != "" {
			out.Ignored = append(out.Ignored, fmt.Sprintf("[profiles.%s] %s composed/prompt/encoding", pname, sname))
		}
		return s
	}
	base := profiles["default"]
	for _, pname := range names {
		rp := profiles[pname]
		eff := map[string]DeclaredSecret{}
		if pname != "default" && rp.inherit {
			for sname, e := range base.entries {
				eff[sname] = convert("default", sname, e)
			}
		}
		for sname, e := range rp.entries {
			eff[sname] = convert(pname, sname, e)
		}
		out.Profiles[pname] = eff
		out.Inherits[pname] = pname != "default" && rp.inherit
	}
	// Scopes: names must be declared somewhere.
	declared := map[string]bool{}
	for _, eff := range out.Profiles {
		for sname := range eff {
			declared[sname] = true
		}
	}
	for scope, sc := range doc.Scopes {
		for _, sname := range sc.Secrets {
			if !declared[sname] {
				return nil, fmt.Errorf("[scopes.%s] names %s, which no profile declares", scope, sname)
			}
		}
		out.Scopes[scope] = append([]string{}, sc.Secrets...)
	}
	sort.Strings(out.Ignored)
	return out, nil
}

// ProfileNames lists the declared profiles, default first.
func (s *Spec) ProfileNames() []string {
	var out []string
	for n := range s.Profiles {
		if n != "default" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	if _, ok := s.Profiles["default"]; ok {
		out = append([]string{"default"}, out...)
	}
	return out
}

// SecretsIn lists a profile's secrets in name order.
func (s *Spec) SecretsIn(profile string) []DeclaredSecret {
	eff := s.Profiles[profile]
	names := make([]string, 0, len(eff))
	for n := range eff {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]DeclaredSecret, 0, len(names))
	for _, n := range names {
		out = append(out, eff[n])
	}
	return out
}

// ConsumersOf: which consumers receive a secret. No scopes → everyone
// (the empty slice with all=true).
func (s *Spec) ConsumersOf(name string) (consumers []string, all bool) {
	if len(s.Scopes) == 0 {
		return nil, true
	}
	for scope, names := range s.Scopes {
		for _, n := range names {
			if n == name {
				consumers = append(consumers, scope)
			}
		}
	}
	sort.Strings(consumers)
	return consumers, false
}

// CheckScopesAgainst verifies every scope names a consumer the contract has:
// an app part by name, or an init step as init-<name>. A typo here would
// silently starve a container of its secrets, so it is a refusal.
func (s *Spec) CheckScopesAgainst(c *appcontract.Contract) error {
	if len(s.Scopes) == 0 {
		return nil
	}
	have := map[string]bool{}
	for _, p := range c.Parts {
		have[p.Name] = true
	}
	for _, it := range c.Init {
		have["init-"+it.Name] = true
	}
	var bad []string
	for scope := range s.Scopes {
		if !have[scope] {
			bad = append(bad, scope)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		return fmt.Errorf("%s: [scopes.%s] names no app part or init step in .apptown/app.yaml (parts are scoped by name, init steps as init-<name>)", SpecFile, strings.Join(bad, "], [scopes."))
	}
	return nil
}

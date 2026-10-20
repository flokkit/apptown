package appcontract

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The forge-native runtime contract (.apptown/app.yaml, design/decisions/forge-native-contract.md).
// The source of truth: what the app IS (app: a map of parts) and what it DEPENDS
// on (needs: a map of catalog capabilities). Compose is no longer the contract —
// it is generated from this for local dev (compose_gen.go). No x- prefixes: the
// whole file is forge's, so app parts use plain compose-style keys and forge
// concepts (dataDir, public) are plain fields.
//
//	app:
//	  web:   { build: ., ports: ["8080"], public: true, dataDir: /data }
//	  worker:{ build: ., command: ["node","worker.js"] }
//	needs:
//	  postgres: { version: "16", properties: { shared_buffers: 256MB } }

type appYamlPart struct {
	Build       string         `yaml:"build"`
	Image       string         `yaml:"image"`
	Static      appYamlStatic  `yaml:"static"`
	Command     any            `yaml:"command"` // string or []string (compose)
	Ports       []string       `yaml:"ports"`
	Environment map[string]any `yaml:"environment"`
	DataDir     string         `yaml:"dataDir"`
	Seed        string         `yaml:"seed"`        // reserved: parsed, run nowhere
	Healthcheck string         `yaml:"healthcheck"` // LEGACY: a shell command; exit 0 = healthy (ECS healthCheck)
	Health      *Health        `yaml:"health"`      // structured per-part health intent (design/subsystems/healthchecks.md); additive over Healthcheck
	Public      bool           `yaml:"public"`
	Replicas    any            `yaml:"replicas"` // omitted/null or 1; larger counts are not supported
	// Resources is the most the part may use (resources.go); none is no limit.
	Resources *appYamlResources `yaml:"resources"`
}

// appYamlStatic is a static part's source: a committed folder (the string
// form), or a declared build whose output folder is served (the map form).
//
//	static: public
//	static: { build: ., target: site, output: dist }
type appYamlStatic struct {
	Folder string `yaml:"-"`
	Build  string `yaml:"build"`
	Target string `yaml:"target"`
	Output string `yaml:"output"`
}

func (s *appYamlStatic) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&s.Folder)
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: static: is a folder, or a map of build:, target: and output:", n.Line)
	}
	// A node decodes without the document's KnownFields: refuse unknown
	// keys here, as the rest of the file does.
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch k := n.Content[i].Value; k {
		case "build", "target", "output":
		default:
			return fmt.Errorf("line %d: static: has no field %q (build, target, output)", n.Content[i].Line, k)
		}
	}
	type plain appYamlStatic
	return n.Decode((*plain)(s))
}

// declared says whether the part names a static source at all.
func (s appYamlStatic) declared() bool {
	return s.Folder != "" || s.Build != "" || s.Output != "" || s.Target != ""
}

// staticBuild validates the map form's spelling; the exported files
// themselves are checked after the build (docker.PackStaticBuild).
func (s appYamlStatic) staticBuild(part string) (*StaticBuild, error) {
	if s.Folder != "" || !s.declared() {
		return nil, nil
	}
	if s.Build == "" || s.Output == "" {
		return nil, fmt.Errorf("app part %q: static: needs build: (the folder with the Dockerfile) and output: (the folder the build writes, e.g. dist)", part)
	}
	out := filepath.ToSlash(filepath.Clean(s.Output))
	if filepath.IsAbs(s.Output) || strings.HasPrefix(s.Output, "/") || out == ".." || strings.HasPrefix(out, "../") {
		return nil, fmt.Errorf("app part %q: static: output: %q must be a folder inside the build stage's files, written relative (dist)", part, s.Output)
	}
	return &StaticBuild{Context: s.Build, Target: s.Target, Output: out}, nil
}

// appYamlInit is a run-to-completion container that must succeed BEFORE the app
// parts start (a migration, an asset build). Ordered: init runs top-to-bottom.
// Maps to ECS as essential:false containers chained by dependsOn: SUCCESS, with
// the app parts depending on the last one; to compose as one-shot services the
// app depends_on with condition: service_completed_successfully.
type appYamlInit struct {
	Name        string         `yaml:"name"`
	Build       string         `yaml:"build"`
	Image       string         `yaml:"image"`
	Command     any            `yaml:"command"`
	Environment map[string]any `yaml:"environment"`
}

type appYamlDoc struct {
	// Version is the contract's shape version. Absent means 1 (files that
	// predate versioning); an explicit 0 is invalid; a version this binary
	// does not know is a clear error, never a silent partial decode.
	Version *int                      `yaml:"version"`
	App     map[string]appYamlPart    `yaml:"app"`
	Init    []appYamlInit             `yaml:"init"`
	Needs   map[string]map[string]any `yaml:"needs"`
	// Secrets is the forge's own metadata about the app's declared secrets
	//: the declaration itself is secretspec.toml.
	Secrets *appYamlSecrets `yaml:"secrets"`
	// Rehearsal is what a restore rehearsal asks the app besides its
	// readiness and its front page (design/subsystems/deployment.md,
	// "Restore rehearsals").
	Rehearsal *appYamlRehearsal `yaml:"rehearsal"`
	// Profiles change parts' resources and environment for the deployments
	// that use them (resources.go).
	Profiles map[string]map[string]appYamlProfilePart `yaml:"profiles"`
}

// appYamlRehearsal: the data checks a restore rehearsal runs.
//
//	rehearsal:
//	  checks: ["GET /healthz/data", "GET /api/orders?limit=1 expect 200"]
type appYamlRehearsal struct {
	Checks []string `yaml:"checks"`
}

// appYamlSecrets: what SecretSpec does not model and the forge must know.
//
//	secrets:
//	  spec: secretspec.toml          # default
//	  generate:
//	    SECRET_KEY_BASE: deployment   # stability domain: deployment | project
//	  deliver:
//	    CERT_PEM: file                # env (default) | file (not built yet)
type appYamlSecrets struct {
	Spec     string            `yaml:"spec"`
	Generate map[string]string `yaml:"generate"`
	Deliver  map[string]string `yaml:"deliver"`
}

// currentContractVersion is the highest .apptown/app.yaml shape this binary
// understands.
const currentContractVersion = 1

var secretNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Parse parses .apptown/app.yaml into the internal contract. Errors
// name the offending field and are written for a human or an agent to fix.
func Parse(raw []byte) (*Contract, error) {
	var doc appYamlDoc
	// Strict: an unknown key is a wrong key (a misspelled `healthcheck` must
	// fail, not silently produce an incomplete contract). yaml.v3's
	// KnownFields rejects fields absent from the structs above.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf(".apptown/app.yaml: %w", err)
	}
	// Version rules: absent == 1 (pre-version files); explicit 0 is invalid;
	// anything past what we know needs a newer forge.
	switch {
	case doc.Version == nil:
		// pre-version file — treat as v1
	case *doc.Version == 0:
		return nil, fmt.Errorf(".apptown/app.yaml: version: 0 is invalid — omit version (means 1) or set the version your contract is written for")
	case *doc.Version > currentContractVersion:
		return nil, fmt.Errorf(".apptown/app.yaml: contract version %d requires a newer forge (this build understands up to %d)", *doc.Version, currentContractVersion)
	}
	if len(doc.App) == 0 {
		return nil, fmt.Errorf(".apptown/app.yaml: no app parts declared under `app:`")
	}

	var c Contract
	names := make([]string, 0, len(doc.App))
	for n := range doc.App {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic order + stable errors

	publicCount := 0
	for _, name := range names {
		p := doc.App[name]
		if p.Replicas != nil {
			replicas, ok := p.Replicas.(int)
			if !ok || replicas < 1 {
				return nil, fmt.Errorf(".apptown/app.yaml: app part %q: replicas must be a positive integer", name)
			}
			if replicas > 1 {
				return nil, fmt.Errorf(".apptown/app.yaml: app part %q: replicas: %d is not supported yet; only 1 replica per app part is supported", name, replicas)
			}
		}
		sources := 0
		for _, s := range []string{p.Build, p.Image} {
			if s != "" {
				sources++
			}
		}
		if p.Static.declared() {
			sources++
		}
		if sources != 1 {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q must declare exactly one of build:, image:, or static: (got %d)", name, sources)
		}
		if err := validateHealth(name, p.Health); err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: %w", err)
		}
		if _, reserved := p.Environment[BrowserEnvVar]; reserved {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q sets %s, which only AppTown sets", name, BrowserEnvVar)
		}
		built, err := p.Static.staticBuild(name)
		if err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: %w", err)
		}
		if built != nil && !p.Public {
			// The forge builds and serves a static site as the front door;
			// a second, built one beside it has no use case yet.
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: a static site with a build must be the public part", name)
		}
		resources, err := p.Resources.parse()
		if err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: %w", name, err)
		}
		part := Part{
			Resources: resources,
			Name:      name, Build: p.Build, Image: p.Image, Static: p.Static.Folder, StaticBuild: built,
			DataDir: p.DataDir, Seed: p.Seed, Health: p.Healthcheck, Intent: p.Health, Public: p.Public,
			Command: partCommand(p.Command),
			Env:     stringifyMap(p.Environment),
		}
		port, err := containerPort(p.Ports)
		if err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: %w", name, err)
		}
		// Default the public port once, in the canonical declaration. Workers
		// without ports stay workers; static servers listen on 80.
		if port == 0 && (p.Public || p.Static.declared()) {
			port = 8080
			if p.Static.declared() {
				port = 80
			}
		}
		part.Port = port
		c.Parts = append(c.Parts, part)
		if p.Public {
			publicCount++
		}
	}
	if publicCount != 1 {
		return nil, fmt.Errorf(".apptown/app.yaml: exactly one app part must be marked `public: true` (got %d) — it is the front door the preview/deploy URL points at", publicCount)
	}

	// init: ordered run-to-completion containers that must succeed before the
	// app parts start.
	for i, it := range doc.Init {
		name := it.Name
		if name == "" {
			name = fmt.Sprintf("init-%d", i+1)
		}
		sources := 0
		for _, s := range []string{it.Build, it.Image} {
			if s != "" {
				sources++
			}
		}
		if sources != 1 {
			return nil, fmt.Errorf(".apptown/app.yaml: init %q must declare exactly one of build: or image:", name)
		}
		c.Init = append(c.Init, InitContainer{
			Name: name, Build: it.Build, Image: it.Image,
			Command: partCommand(it.Command), Env: stringifyMap(it.Environment),
		})
	}

	// needs: a map of capability → {typed params + a properties escape hatch}.
	if err := parseAppYamlNeeds(doc.Needs, &c); err != nil {
		return nil, fmt.Errorf(".apptown/app.yaml: needs: %w", err)
	}
	// A part and a need share the stack's network, each aliased by its
	// name, and a profile names either: one name is one of them.
	for _, p := range c.Parts {
		if slices.Contains(c.Needs, p.Name) {
			return nil, fmt.Errorf(".apptown/app.yaml: %q is both a part under app: and a need: rename the part", p.Name)
		}
	}
	if doc.Secrets != nil {
		c.SecretSpecPath = doc.Secrets.Spec
		for name, domain := range doc.Secrets.Generate {
			if !secretNameRe.MatchString(name) {
				return nil, fmt.Errorf(".apptown/app.yaml: secrets: generate: %q is not an environment variable name", name)
			}
			if domain == "environment" {
				return nil, fmt.Errorf(".apptown/app.yaml: secrets: generate: %s: the stability domain once called environment is now called deployment: write %s: deployment", name, name)
			}
			if domain != "deployment" && domain != "project" {
				return nil, fmt.Errorf(".apptown/app.yaml: secrets: generate: %s: %q is not a stability domain (deployment | project)", name, domain)
			}
			if c.SecretGenerate == nil {
				c.SecretGenerate = map[string]string{}
			}
			c.SecretGenerate[name] = domain
		}
		for name, channel := range doc.Secrets.Deliver {
			if channel == "env" {
				continue
			}
			if channel == "file" {
				return nil, fmt.Errorf(".apptown/app.yaml: secrets: deliver: %s: file delivery is not built yet — secrets reach the app as environment variables", name)
			}
			return nil, fmt.Errorf(".apptown/app.yaml: secrets: deliver: %s: %q is not a delivery channel (env)", name, channel)
		}
	}
	if err := parseProfiles(doc.Profiles, &c); err != nil {
		return nil, fmt.Errorf(".apptown/app.yaml: %w", err)
	}
	for _, name := range append([]string{DefaultProfile}, c.ProfileNames()...) {
		if err := c.ForProfile(name).CheckNeedSizes(); err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: profile %s: %w", name, err)
		}
	}
	if doc.Rehearsal != nil {
		for _, line := range doc.Rehearsal.Checks {
			if _, _, err := ParseDataCheck(line); err != nil {
				return nil, fmt.Errorf(".apptown/app.yaml: rehearsal: checks: %w", err)
			}
			c.RehearsalChecks = append(c.RehearsalChecks, strings.Join(strings.Fields(line), " "))
		}
	}
	return &c, nil
}

// ParseDataCheck reads one rehearsal data check: "GET <path>", answered
// 200, or "GET <path> expect <status>". It is the GET line of the probe
// grammar (internal/check), and only that: a data check asks one path.
func ParseDataCheck(line string) (path string, expect int, err error) {
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "GET" || !strings.HasPrefix(f[1], "/") {
		return "", 0, fmt.Errorf("%q: a data check is \"GET /path\" or \"GET /path expect <status>\"", line)
	}
	switch {
	case len(f) == 2:
		return f[1], 200, nil
	case len(f) == 4 && f[2] == "expect":
		n, err := strconv.Atoi(f[3])
		if err != nil || n < 100 || n > 599 {
			return "", 0, fmt.Errorf("%q: expect wants an HTTP status", line)
		}
		return f[1], n, nil
	}
	return "", 0, fmt.Errorf("%q: a data check is \"GET /path\" or \"GET /path expect <status>\"", line)
}

// parseAppYamlNeeds turns the needs map into c.Needs + c.NeedParams. Typed
// scalar fields and scalar entries of the `properties:` escape hatch both become
// interpolation params ({name} tokens); properties lets a need carry esoteric,
// un-anticipated provisioning params without a schema change.
func parseAppYamlNeeds(needs map[string]map[string]any, c *Contract) error {
	if len(needs) == 0 {
		return nil
	}
	names := make([]string, 0, len(needs))
	for n := range needs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		c.Needs = append(c.Needs, name)
		spec := needs[name]
		params := map[string]string{}
		// A parameter that is not a scalar is refused, never dropped: a
		// dropped one would be replaced by the catalog's default.
		// resources is the need's size, typed as a part's: not a
		// parameter, so the catalogue never interpolates it.
		if raw, present := spec["resources"]; present {
			r, err := needResources(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if c.NeedResources == nil {
				c.NeedResources = map[string]Resources{}
			}
			c.NeedResources[name] = r
		}
		if raw, present := spec["size"]; present {
			nameSize, err := ParseSizeName(raw)
			if err != nil {
				return fmt.Errorf("needs: %s: %w", name, err)
			}
			if c.NeedSizes == nil {
				c.NeedSizes = map[string]NeedSize{}
			}
			c.NeedSizes[name] = NeedSize{Name: nameSize}
		}
		for k, v := range spec {
			if k == "properties" || k == "resources" || k == "size" {
				continue
			}
			s, ok := scalarString(v)
			if !ok {
				return fmt.Errorf(".apptown/app.yaml: needs: %s: %s: a parameter is a single value, not %s", name, k, shapeOf(v))
			}
			params[k] = s
		}
		// The escape hatch: properties: { k: v } — scalar values become params
		// too (a property named the same as a typed field would override it).
		props, ok := spec["properties"].(map[string]any)
		if raw, present := spec["properties"]; present && raw != nil && !ok {
			return fmt.Errorf(".apptown/app.yaml: needs: %s: properties: a map of single values, not %s", name, shapeOf(raw))
		}
		for k, v := range props {
			if k == "size" {
				return fmt.Errorf("needs: %s: size is selected directly, not through properties", name)
			}
			s, ok := scalarString(v)
			if !ok {
				return fmt.Errorf(".apptown/app.yaml: needs: %s: properties: %s: a property is a single value, not %s", name, k, shapeOf(v))
			}
			params[k] = s
		}
		// disposable says the need's data is not worth keeping (a cache): it
		// is never copied and starts empty. A misspelt value would silently
		// protect, or silently not protect, so only the two words are read.
		if v, ok := params["disposable"]; ok && v != "true" && v != "false" {
			return fmt.Errorf(".apptown/app.yaml: needs: %s: disposable is true or false, not %q", name, v)
		}
		if len(params) > 0 {
			if c.NeedParams == nil {
				c.NeedParams = map[string]map[string]string{}
			}
			c.NeedParams[name] = params
		}
	}
	return nil
}

// containerPort pulls the container port from a compose-style ports list
// ("8080" or "3000:8080" → 8080). Empty is allowed (a static site, or the
// house default fills in).
func containerPort(ports []string) (int, error) {
	if len(ports) == 0 {
		return 0, nil
	}
	spec := ports[0]
	if i := strings.LastIndex(spec, ":"); i >= 0 {
		spec = spec[i+1:]
	}
	spec = strings.TrimSuffix(strings.TrimSpace(spec), "/tcp")
	n, err := strconv.Atoi(spec)
	if err != nil {
		return 0, fmt.Errorf("ports: %q is not a container port", ports[0])
	}
	return n, nil
}

func partCommand(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{"sh", "-c", x} // a bare string is a shell command (compose)
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	}
	return nil
}

func stringifyMap(m map[string]any) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// scalarString reports whether v is one of the scalars YAML decodes to
// (a string, number, boolean or timestamp) and its string form. Anything
// else (a map with any key type, a list, empty) is not a parameter value.
func scalarString(v any) (string, bool) {
	switch v.(type) {
	case string, bool, int, int64, uint64, float64, time.Time:
		return fmt.Sprint(v), true
	}
	return "", false
}

// shapeOf names what a value that is not a scalar is.
func shapeOf(v any) string {
	switch v.(type) {
	case map[string]any, map[any]any:
		return "a map"
	case []any:
		return "a list"
	case nil:
		return "empty"
	}
	return fmt.Sprintf("%T", v)
}

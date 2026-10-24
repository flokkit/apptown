package appcontract

import (
	"bytes"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AppTown-native runtime contract (.apptown/app.yaml, design/decisions/forge-native-contract.md).
// The source of truth: what the app IS (app: a map of parts) and what it DEPENDS
// on (needs: a map of catalog capabilities). Compose is no longer the contract —
// it is generated from this for local dev (compose_gen.go). No x- prefixes: the
// whole file is AppTown's, so app parts use plain compose-style keys and AppTown
// concepts (dataDir, public) are plain fields.
//
//	app:
//	  web:   { build: ., ports: ["8080"], public: true, dataDir: /data }
//	  worker:{ build: ., command: ["node","worker.js"] }
//	needs:
//	  postgres: { version: "16" }

type appYamlPart struct {
	Build       string        `yaml:"build"`
	Image       string        `yaml:"image"`
	Static      appYamlStatic `yaml:"static"`
	Command     any           `yaml:"command"` // string or []string (compose)
	Ports       []string      `yaml:"ports"`
	Environment environment   `yaml:"environment"`
	DataDir     string        `yaml:"dataDir"`
	// Seed is refused by name (no target runs it yet), so a target that
	// learns to run it later changes no app that wrote it.
	Seed        string  `yaml:"seed"`
	Healthcheck string  `yaml:"healthcheck"` // a non-public part's container health check (a shell command; exit 0 = healthy); refused on the public part
	Health      *Health `yaml:"health"`      // structured per-part health intent (design/subsystems/healthchecks.md)
	Public      bool    `yaml:"public"`
	// Tools names what the part is built with (go, node): the checks for
	// a tool judge the parts that name it, in the part's folder.
	Tools    []string `yaml:"tools"`
	Replicas any      `yaml:"replicas"` // omitted/null or 1; larger counts are not supported
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
	Name        string      `yaml:"name"`
	Build       string      `yaml:"build"`
	Image       string      `yaml:"image"`
	Command     any         `yaml:"command"`
	Environment environment `yaml:"environment"`
}

type appYamlDoc struct {
	// Version is the contract's shape version. Absent means 1 (files that
	// predate versioning); a nonpositive value is invalid; a version this binary
	// does not know is a clear error, never a silent partial decode.
	Version *int                      `yaml:"version"`
	App     map[string]appYamlPart    `yaml:"app"`
	Init    []appYamlInit             `yaml:"init"`
	Needs   map[string]map[string]any `yaml:"needs"`
	// Secrets is AppTown's own metadata about the app's declared secrets
	//: the declaration itself is secretspec.toml.
	Secrets *appYamlSecrets `yaml:"secrets"`
	// Rehearsal is what a restore rehearsal asks the app besides its
	// readiness and its front page (design/subsystems/deployment.md,
	// "Restore rehearsals").
	Rehearsal *appYamlRehearsal `yaml:"rehearsal"`
	// Profiles change parts' resources and environment for the deployments
	// that use them (resources.go).
	Profiles map[string]map[string]appYamlProfilePart `yaml:"profiles"`
	// NotAppCode lists folders whose code is not the app's (docs, tooling),
	// relative to the repository root: the contract check does not ask for
	// their manifests to be declared.
	NotAppCode []string `yaml:"notAppCode"`
}

// appYamlRehearsal: the data checks a restore rehearsal runs.
//
//	rehearsal:
//	  checks: ["GET /healthz/data", "GET /api/orders?limit=1 expect 200"]
type appYamlRehearsal struct {
	Checks []string `yaml:"checks"`
}

// appYamlSecrets: what SecretSpec does not model and AppTown must know.
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
// understands. Version 2 writes memory in Kubernetes binary units only and
// may wire need values into environment values; version 1 (or no version)
// keeps its own meaning wherever it is read, a recorded revision included.
const currentContractVersion = 2

var secretNameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// partNameRe is the name of a part or an init step: App Platform's
// component name rule, the strictest of the targets (parts are its services
// and workers, init steps its jobs, under these names).
var partNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// checkPartName refuses a part or init step name no target can carry, or
// one a target already gives something of its own: Portainer's service for
// the public part is app and its init services are init-<name>, and
// AppTown's own components (App Platform's apptown-gate) start apptown-.
func checkPartName(kind, name string) error {
	switch {
	case !partNameRe.MatchString(name):
		return fmt.Errorf("%s %q: a name is 2 to 32 lower-case letters, digits and hyphens, starting with a letter and not ending with a hyphen", kind, name)
	case name == "app":
		return fmt.Errorf("%s %q: app is the name a target gives the public part; choose another", kind, name)
	case strings.HasPrefix(name, "init-"), strings.HasPrefix(name, "apptown-"):
		return fmt.Errorf("%s %q: names starting init- or apptown- are the ones a target gives its own services; choose another", kind, name)
	}
	return nil
}

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
	// Version rules: absent == 1 (pre-version files); explicit nonpositive values are invalid;
	// anything past what we know needs a newer AppTown.
	switch {
	case doc.Version == nil:
		// pre-version file — treat as v1
	case *doc.Version <= 0:
		return nil, fmt.Errorf(".apptown/app.yaml: version: %d is invalid — omit version (means 1) or set 1 or 2 for the contract you intend", *doc.Version)
	case *doc.Version > currentContractVersion:
		return nil, fmt.Errorf(".apptown/app.yaml: contract version %d requires a newer AppTown (this build understands up to %d)", *doc.Version, currentContractVersion)
	}
	if len(doc.App) == 0 {
		return nil, fmt.Errorf(".apptown/app.yaml: no app parts declared under `app:`")
	}

	var c Contract
	c.Version = 1
	if doc.Version != nil {
		c.Version = *doc.Version
	}
	v1 := c.Version == 1
	names := make([]string, 0, len(doc.App))
	for n := range doc.App {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic order + stable errors

	publicCount := 0
	for _, name := range names {
		p := doc.App[name]
		if err := checkPartName("app part", name); err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: %w", err)
		}
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
		// Refused rather than accepted and ignored: each would gain a meaning
		// when a target learns it, and silently change the apps that wrote it.
		if p.Seed != "" {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: seed: is not run by any target yet; remove it, and run the command as an init step (init:) if it must run at every deploy", name)
		}
		if p.Healthcheck != "" && p.Public {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: healthcheck: is not used on the public part; remove it and declare health: { readiness: { http: /your-path } }", name)
		}
		if p.DataDir != "" && !p.Public {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: dataDir: is kept only for the public part; keep durable files in the public part's dataDir or in a need", name)
		}
		if env := setsAppTownEnv(p.Environment); env != "" {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q sets %s in environment:, which only AppTown sets; remove it and read the value AppTown gives", name, env)
		}
		if p.Static.declared() {
			// AppTown's file server serves a static site: it reads none of
			// these, so each would be a setting that silently does nothing.
			for _, f := range []struct {
				field string
				set   bool
			}{{"environment", len(p.Environment) > 0}, {"command", p.Command != nil}, {"dataDir", p.DataDir != ""}, {"healthcheck", p.Healthcheck != ""}} {
				if f.set {
					return nil, fmt.Errorf(".apptown/app.yaml: app part %q: %s: is not used by a static site (AppTown's file server serves its files); remove it", name, f.field)
				}
			}
		}
		env, err := p.Environment.read(true)
		if err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: %w", name, err)
		}
		built, err := p.Static.staticBuild(name)
		if err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: %w", err)
		}
		if built != nil && !p.Public {
			// AppTown builds and serves a static site as the front door;
			// a second, built one beside it has no use case yet.
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: a static site with a build must be the public part", name)
		}
		resources, err := p.Resources.parse(v1)
		if err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: %w", name, err)
		}
		if err := checkTools(p); err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: app part %q: %w", name, err)
		}
		part := Part{
			Tools:     slices.Clone(p.Tools),
			Resources: resources,
			Name:      name, Build: p.Build, Image: p.Image, Static: p.Static.Folder, StaticBuild: built,
			DataDir: p.DataDir, Health: p.Healthcheck, Intent: p.Health, Public: p.Public,
			Command: partCommand(p.Command),
			Env:     env,
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
	notApp, err := checkNotAppCode(doc.NotAppCode, c.Parts)
	if err != nil {
		return nil, fmt.Errorf(".apptown/app.yaml: %w", err)
	}
	c.NotAppCode = notApp

	// init: ordered run-to-completion containers that must succeed before the
	// app parts start.
	initNames := map[string]bool{}
	for i, it := range doc.Init {
		name := it.Name
		// A name is required: a secret scoped to an init step names it, and a
		// name given by position would move to another step when one is
		// added above it.
		if name == "" {
			return nil, fmt.Errorf(".apptown/app.yaml: init step %d has no name: give it one (name: migrate)", i+1)
		}
		if err := checkPartName("init step", name); err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: %w", err)
		}
		if initNames[name] {
			return nil, fmt.Errorf(".apptown/app.yaml: two init steps are named %q", name)
		}
		initNames[name] = true
		if _, ok := doc.App[name]; ok {
			return nil, fmt.Errorf(".apptown/app.yaml: %q is both a part under app: and an init step: rename one", name)
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
		env, err := it.Environment.read(false)
		if err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: init step %q: %w", name, err)
		}
		c.Init = append(c.Init, InitContainer{
			Name: name, Build: it.Build, Image: it.Image,
			Command: partCommand(it.Command), Env: env,
		})
	}

	// needs: a map of capability → its parameters (the catalogue says which a kind takes).
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
	for _, it := range c.Init {
		if slices.Contains(c.Needs, it.Name) {
			return nil, fmt.Errorf(".apptown/app.yaml: %q is both an init step and a need: rename the init step", it.Name)
		}
	}
	if doc.Secrets != nil {
		if err := CheckSpecPath(doc.Secrets.Spec); err != nil {
			return nil, fmt.Errorf(".apptown/app.yaml: secrets: spec: %w", err)
		}
		c.SecretSpecPath = doc.Secrets.Spec
		for name, domain := range doc.Secrets.Generate {
			if !secretNameRe.MatchString(name) {
				return nil, fmt.Errorf(".apptown/app.yaml: secrets: generate: %q is not an environment variable name", name)
			}
			if why := ReservedEnvName(name); why != "" {
				return nil, fmt.Errorf(".apptown/app.yaml: secrets: generate: %s is AppTown's (%s); name the secret something else", name, why)
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
	if err := parseProfiles(doc.Profiles, &c, v1); err != nil {
		return nil, fmt.Errorf(".apptown/app.yaml: %w", err)
	}
	if err := checkNeedRefs(&c); err != nil {
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

// parseAppYamlNeeds turns the needs map into c.Needs + c.NeedParams: each
// scalar field becomes an interpolation param ({name} token). Which params a
// kind takes is the catalogue's (dependency.RefusesDeclaration); the old
// properties: pass-through is refused here.
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
			r, err := needResources(raw, c.Version == 1)
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
		if _, present := spec["properties"]; present {
			return fmt.Errorf("%s: properties: is not read; write each parameter the need takes directly under needs.%s", name, name)
		}
		for k, v := range spec {
			if k == "resources" || k == "size" {
				continue
			}
			s, ok := scalarString(v)
			if !ok {
				return fmt.Errorf(".apptown/app.yaml: needs: %s: %s: a parameter is a single value, not %s", name, k, shapeOf(v))
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

// containerPort reads the one port a part listens on ("8080", or
// "8080/tcp"). Empty is allowed (a static site, or the house default fills
// in). A part has one port, and AppTown chooses where it is published, so a
// second entry or a host half ("3000:8080") would be a setting that does
// nothing.
func containerPort(ports []string) (int, error) {
	if len(ports) == 0 {
		return 0, nil
	}
	if len(ports) > 1 {
		return 0, fmt.Errorf("ports: a part listens on one port; list only that one (got %d)", len(ports))
	}
	spec := ports[0]
	if strings.Contains(spec, ":") {
		return 0, fmt.Errorf("ports: %q names a host port, and AppTown chooses where a part is published; write the port the part listens on alone", spec)
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

// CheckSpecPath refuses a secrets.spec path that is absolute or leaves the
// project's tree: the declaration is read from the app's own source.
func CheckSpecPath(path string) error {
	clean := filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%q must be a file inside the project, written relative (secretspec.toml)", path)
	}
	return nil
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

// toolName is how a tool is spelled in a part's tools: lower-case letters,
// digits and dashes, starting with a letter (the tool versions' names).
var toolName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// checkTools holds a part's tools to their spelling: no repeats, and only
// on a part that has a folder to judge (not a pulled image).
func checkTools(p appYamlPart) error {
	if len(p.Tools) == 0 {
		return nil
	}
	if p.Image != "" {
		return fmt.Errorf("tools: names what a part is built with from its folder, and an image: part has none; remove it")
	}
	seen := map[string]bool{}
	for _, t := range p.Tools {
		if !toolName.MatchString(t) {
			return fmt.Errorf("tools: %q is not a tool name (go, node, python: lower-case letters, digits and dashes)", t)
		}
		if seen[t] {
			return fmt.Errorf("tools: names %s twice", t)
		}
		seen[t] = true
	}
	return nil
}

// checkNotAppCode cleans the folders that are not the app's code: each is
// relative, inside the repository, and neither a part's folder nor one
// holding it (that would declare the part's own code not the app's).
func checkNotAppCode(folders []string, parts []Part) ([]string, error) {
	var out []string
	for _, f := range folders {
		c := path.Clean(strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(f)), "./"))
		if c == "" || c == "." || path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") {
			return nil, fmt.Errorf("notAppCode: %q must be a folder inside the repository, written relative (docs/site)", f)
		}
		for i := range parts {
			pf := parts[i].Folder()
			if pf == "" {
				continue
			}
			if strings.HasPrefix(pf+"/", c+"/") {
				return nil, fmt.Errorf("notAppCode: %q holds part %s's folder %s, so it would say the part's own code is not the app's", f, parts[i].Name, pf)
			}
		}
		if slices.Contains(out, c) {
			return nil, fmt.Errorf("notAppCode: names %s twice", c)
		}
		out = append(out, c)
	}
	return out, nil
}

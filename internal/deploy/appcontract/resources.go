package appcontract

import (
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Resources is the most a part may use. Zero in a field is no limit of that
// kind in a declaration or historical applied record. Fresh container execution
// supplies defaults separately; zero is never proof a running container is capped.
type Resources struct {
	MemoryBytes int64   `json:"memoryBytes,omitempty"` // swap included
	CPUs        float64 `json:"cpus,omitempty"`
}

// Declared says whether any limit is declared.
func (r Resources) Declared() bool { return r.MemoryBytes > 0 || r.CPUs > 0 }

// Over is r with each field other declares replacing r's: a profile that
// declares memory alone keeps the base's cpu.
func (r Resources) Over(other Resources) Resources {
	if other.MemoryBytes > 0 {
		r.MemoryBytes = other.MemoryBytes
	}
	if other.CPUs > 0 {
		r.CPUs = other.CPUs
	}
	return r
}

// MemoryWords is the memory as app.yaml writes it, or "no limit".
func (r Resources) MemoryWords() string {
	if r.MemoryBytes <= 0 {
		return "no limit"
	}
	return FormatMemory(r.MemoryBytes)
}

// CPUWords is the cpu as app.yaml writes it, or "no limit".
func (r Resources) CPUWords() string {
	if r.CPUs <= 0 {
		return "no limit"
	}
	return strconv.FormatFloat(r.CPUs, 'f', -1, 64)
}

// MinMemoryBytes is the smallest memory a part may declare: below it a
// container cannot start reliably.
const MinMemoryBytes = 32 << 20

// ParseMemory reads a memory as the current contract writes it, in
// Kubernetes binary units: a whole number of mebibytes or gibibytes,
// "512Mi", "1Gi". A bare "512M" is refused, not guessed: version 1 read it
// as MiB, Kubernetes and Score read it as decimal megabytes.
func ParseMemory(s string) (int64, error) { return parseMemory(s, false) }

// ParseMemoryV1 reads a memory as a version-1 contract or a pack default
// writes it: "Mi"/"Gi", or "M"/"G" meaning the same (MiB, GiB).
func ParseMemoryV1(s string) (int64, error) { return parseMemory(s, true) }

func parseMemory(s string, bareMeansBinary bool) (int64, error) {
	v := strings.TrimSpace(s)
	unit := int64(0)
	type memoryUnit struct {
		suffix string
		bytes  int64
	}
	units := []memoryUnit{{"Mi", 1 << 20}, {"Gi", 1 << 30}}
	if bareMeansBinary {
		units = append(units, memoryUnit{"M", 1 << 20}, memoryUnit{"G", 1 << 30})
	}
	for _, u := range units {
		if strings.HasSuffix(v, u.suffix) {
			v, unit = strings.TrimSuffix(v, u.suffix), u.bytes
			break
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if unit == 0 && !bareMeansBinary {
		if m := bareMemoryRe.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
			return 0, fmt.Errorf("memory %q: write %s%si (version 1 read M and G as MiB and GiB; Kubernetes and Score read them as decimal)", s, m[1], m[2])
		}
	}
	if unit == 0 || err != nil || n <= 0 {
		if bareMeansBinary {
			return 0, fmt.Errorf("memory %q is a whole number of M or G (MiB, GiB) or of Mi or Gi, such as 512M or 1Gi", s)
		}
		return 0, fmt.Errorf("memory %q is a whole number of Mi or Gi, such as 512Mi or 1Gi", s)
	}
	if n > (1<<40)/unit {
		return 0, fmt.Errorf("memory %q is more than any host has", s)
	}
	if n*unit < MinMemoryBytes {
		return 0, fmt.Errorf("memory %q is below the smallest a part may have (32Mi)", s)
	}
	return n * unit, nil
}

var bareMemoryRe = regexp.MustCompile(`^(\d+)\s*([MG])$`)

// FormatMemory writes bytes as app.yaml does: Gi when whole, else Mi.
func FormatMemory(b int64) string {
	if b%(1<<30) == 0 {
		return fmt.Sprintf("%dGi", b>>30)
	}
	return fmt.Sprintf("%dMi", b>>20)
}

// ParseCPU reads a cpu as app.yaml writes it: a positive number of CPUs
// ("0.5", "2"), or Kubernetes millicores ("500m").
func ParseCPU(v any) (float64, error) {
	s, ok := scalarString(v)
	if !ok {
		return 0, fmt.Errorf("cpu is a number of CPUs, such as 0.5 or 2, or millicores such as 500m, not %s", shapeOf(v))
	}
	t := strings.TrimSpace(s)
	scale := 1.0
	if strings.HasSuffix(t, "m") {
		t, scale = strings.TrimSuffix(t, "m"), 1000
		if _, err := strconv.ParseInt(t, 10, 64); err != nil {
			return 0, fmt.Errorf("cpu %q: millicores are a whole number, such as 500m", s)
		}
	}
	n, err := strconv.ParseFloat(t, 64)
	n /= scale
	if err != nil || n <= 0 || n > 256 {
		return 0, fmt.Errorf("cpu %q is a positive number of CPUs, such as 0.5 or 2, or millicores such as 500m", s)
	}
	return n, nil
}

type appYamlResources struct {
	Memory string `yaml:"memory"`
	CPU    any    `yaml:"cpu"`
}

func (r *appYamlResources) parse(v1 bool) (Resources, error) {
	var out Resources
	if r == nil {
		return out, nil
	}
	if r.Memory == "" && r.CPU == nil {
		return out, fmt.Errorf("resources: declares neither memory nor cpu")
	}
	if r.Memory != "" {
		m, err := parseMemory(r.Memory, v1)
		if err != nil {
			return out, fmt.Errorf("resources: %w", err)
		}
		out.MemoryBytes = m
	}
	if r.CPU != nil {
		c, err := ParseCPU(r.CPU)
		if err != nil {
			return out, fmt.Errorf("resources: %w", err)
		}
		out.CPUs = c
	}
	return out, nil
}

// appYamlProfilePart is what a profile may change of one part: its
// resources and environment, or a catalogue size for a need.
type appYamlProfilePart struct {
	Size        any               `yaml:"size"`
	Resources   *appYamlResources `yaml:"resources"`
	Environment environment       `yaml:"environment"`
}

// ProfilePart is one profile's change to one part.
type ProfilePart struct {
	Size      string            `json:"size,omitempty"`
	Resources Resources         `json:"resources,omitzero"`
	Env       map[string]string `json:"env,omitempty"`
}

// DefaultProfile is the base: the parts as `app:` declares them.
const DefaultProfile = "default"

// PreviewProfile is the profile a preview uses when a file declares it.
const PreviewProfile = "preview"

var profileNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func parseProfiles(doc map[string]map[string]appYamlProfilePart, c *Contract, v1 bool) error {
	if len(doc) == 0 {
		return nil
	}
	parts, static := map[string]bool{}, map[string]bool{}
	for _, p := range c.Parts {
		parts[p.Name] = true
		static[p.Name] = p.IsStatic()
	}
	needs := map[string]bool{}
	for _, n := range c.Needs {
		needs[n] = true
	}
	c.Profiles = map[string]map[string]ProfilePart{}
	for _, name := range sortedKeys(doc) {
		if name == DefaultProfile {
			return fmt.Errorf("profiles: %s is the base: declare its values on the parts under app:", DefaultProfile)
		}
		if !profileNameRe.MatchString(name) {
			return fmt.Errorf("profiles: %q is not a profile name (lower-case letters, digits and hyphens)", name)
		}
		c.Profiles[name] = map[string]ProfilePart{}
		for _, part := range sortedKeys(doc[name]) {
			if !parts[part] && !needs[part] {
				return fmt.Errorf("profiles: %s: %q is not a part under app: or a need", name, part)
			}
			in := doc[name][part]
			if needs[part] && len(in.Environment) > 0 {
				return fmt.Errorf("profiles: %s: %s: a need's environment is the catalogue's; a profile changes only its resources", name, part)
			}
			if static[part] && len(in.Environment) > 0 {
				return fmt.Errorf("profiles: %s: %s: a static site has no environment; AppTown's file server serves its files", name, part)
			}
			var size string
			if in.Size != nil {
				if !needs[part] {
					return fmt.Errorf("profiles: %s: %s: size is only for needs", name, part)
				}
				var err error
				size, err = ParseSizeName(in.Size)
				if err != nil {
					return fmt.Errorf("profiles: %s: %s: %w", name, part, err)
				}
			}
			if in.Resources == nil && len(in.Environment) == 0 && size == "" {
				return fmt.Errorf("profiles: %s: %s: changes neither resources nor environment", name, part)
			}
			if env := setsAppTownEnv(in.Environment); env != "" {
				return fmt.Errorf("profiles: %s: %s: sets %s, which only AppTown sets", name, part, env)
			}
			r, err := in.Resources.parse(v1)
			if err != nil {
				return fmt.Errorf("profiles: %s: %s: %w", name, part, err)
			}
			env, err := in.Environment.read(true)
			if err != nil {
				return fmt.Errorf("profiles: %s: %s: %w", name, part, err)
			}
			c.Profiles[name][part] = ProfilePart{Size: size, Resources: r, Env: env}
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ProfileNames are the profiles app.yaml declares, sorted; the base
// (default) is not among them.
func (c *Contract) ProfileNames() []string { return sortedKeys(c.Profiles) }

// ForProfile is the contract under profile name: each part's resources
// replaced field by field and its environment merged key by key by what
// the profile declares for it. A name app.yaml does not declare, default
// among them, is the base unchanged. It performs no I/O and changes
// nothing of c.
func (c *Contract) ForProfile(name string) *Contract {
	changes := c.Profiles[name]
	if len(changes) == 0 {
		return c
	}
	out := *c
	out.Parts = append([]Part(nil), c.Parts...)
	for i := range out.Parts {
		ch, ok := changes[out.Parts[i].Name]
		if !ok {
			continue
		}
		out.Parts[i].Resources = out.Parts[i].Resources.Over(ch.Resources)
		if len(ch.Env) > 0 {
			out.Parts[i].Env = MergeEnv(out.Parts[i].Env, ch.Env)
		}
	}
	out.NeedResources = overNeeds(c, func(n string) (Resources, bool) { ch, ok := changes[n]; return ch.Resources, ok })
	out.NeedSizes = maps.Clone(c.NeedSizes)
	for name, ch := range changes {
		if ch.Size != "" {
			if out.NeedSizes == nil {
				out.NeedSizes = map[string]NeedSize{}
			}
			out.NeedSizes[name] = NeedSize{Name: ch.Size}
		}
	}
	return &out
}

// overNeeds is c's need resources with each need over names replaced
// field by field; a new map, so c is unchanged.
func overNeeds(c *Contract, over func(string) (Resources, bool)) map[string]Resources {
	var out map[string]Resources
	for _, n := range c.Needs {
		r, declared := c.NeedResources[n]
		if o, ok := over(n); ok {
			r, declared = r.Over(o), true
		}
		if declared && r.Declared() {
			if out == nil {
				out = map[string]Resources{}
			}
			out[n] = r
		}
	}
	return out
}

// WithResources is c with each named part's or need's resources replaced
// field by field by over (an operator's override). What it does not name
// is unchanged; it changes nothing of c.
func (c *Contract) WithResources(over map[string]Resources) *Contract {
	if len(over) == 0 {
		return c
	}
	out := *c
	out.Parts = append([]Part(nil), c.Parts...)
	for i := range out.Parts {
		if r, ok := over[out.Parts[i].Name]; ok {
			out.Parts[i].Resources = out.Parts[i].Resources.Over(r)
		}
	}
	out.NeedResources = overNeeds(c, func(n string) (Resources, bool) { r, ok := over[n]; return r, ok })
	return &out
}

// PartResources is each part's and each need's resources, by name: one
// map, since a name is one or the other.
func (c *Contract) PartResources() map[string]Resources {
	out := make(map[string]Resources, len(c.Parts)+len(c.Needs))
	for _, p := range c.Parts {
		out[p.Name] = p.Resources
	}
	for _, n := range c.Needs {
		out[n] = c.NeedResources[n]
	}
	return out
}

// needResources reads a need's resources: the same two keys, and the
// same rules, as a part's; any other key is refused, never dropped.
func needResources(raw any, v1 bool) (Resources, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return Resources{}, fmt.Errorf("resources: a map of memory and cpu, not %s", shapeOf(raw))
	}
	var in appYamlResources
	for k, v := range m {
		switch k {
		case "memory":
			s, ok := scalarString(v)
			if !ok {
				return Resources{}, fmt.Errorf("resources: memory is a single value, not %s", shapeOf(v))
			}
			in.Memory = s
		case "cpu":
			in.CPU = v
		default:
			return Resources{}, fmt.Errorf("resources: %q is not memory or cpu", k)
		}
	}
	return in.parse(v1)
}

// ProfileChoice is how a deployment's profile was chosen.
type ProfileChoice string

const (
	ProfileChosen  ProfileChoice = "chosen"  // an explicit choice for the deployment
	ProfilePreview ProfileChoice = "preview" // a preview, and a file declares preview
	ProfileBase    ProfileChoice = "default" // nothing else applies
)

// DeploymentProfile is the one profile a deployment uses, whichever file
// declares it: the explicit choice when app.yaml or secretspec.toml
// declares it; otherwise, for the preview deployment, preview when either
// declares it; otherwise default. Each file then applies the name when it
// declares it and its own base when it does not.
func DeploymentProfile(chosen, deployment string, declared map[string]bool) (string, ProfileChoice) {
	if chosen != "" && (declared[chosen] || chosen == DefaultProfile) {
		return chosen, ProfileChosen
	}
	if deployment == PreviewProfile && declared[PreviewProfile] {
		return PreviewProfile, ProfilePreview
	}
	return DefaultProfile, ProfileBase
}

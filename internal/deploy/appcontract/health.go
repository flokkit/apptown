package appcontract

// Structured, per-part health intent (design/subsystems/healthchecks.md). The app declares
// topology-neutral INTENT — what to check, what healthy means, its own startup
// cost and execution policy — and AppTown renders it into a concrete probe per
// deployment, or refuses (design §4). This is ADDITIVE: the legacy
// `healthcheck: <shell command>` (Part.Health) still works; a part that
// declares nothing gets a synthesized default (design §8). Nothing here reaches
// into topology or ingress — that is forge-owned (ingress.go, §4.2).
//
// This file is the schema + validation only. Rendering (cold-boot / docker) and
// the health controller are separate slices.

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ParseHealthDuration accepts Go duration strings ("90s", "10s", "3s"); a plain
// integer would be ambiguous, so require a unit.
func ParseHealthDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("not a duration like \"90s\": %q", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("must not be negative: %q", s)
	}
	return d, nil
}

// Probe is one liveness or readiness probe's intent.
type Probe struct {
	// exactly one target:
	HTTP string `yaml:"http" json:"http,omitempty"` // a path, e.g. /healthz
	TCP  int    `yaml:"tcp" json:"tcp,omitempty"`   // a port (0 = infer from the part)
	Exec []any  `yaml:"exec" json:"exec,omitempty"` // a command run INSIDE the container

	// readiness only:
	RequestContext string `yaml:"requestContext" json:"requestContext,omitempty"` // frontDoor | direct
	Expect         int    `yaml:"expect" json:"expect,omitempty"`                 // HTTP status (default 200)
	ChecksDeps     bool   `yaml:"checksDeps" json:"checksDeps,omitempty"`         // ASSERTION (metadata, not proven)

	// execution policy (design §4.1):
	StartupGrace     string `yaml:"startupGrace" json:"startupGrace,omitempty"` // e.g. 90s (a FLOOR)
	Period           string `yaml:"period" json:"period,omitempty"`
	Timeout          string `yaml:"timeout" json:"timeout,omitempty"`
	FailureThreshold int    `yaml:"failureThreshold" json:"failureThreshold,omitempty"`
	SuccessThreshold int    `yaml:"successThreshold" json:"successThreshold,omitempty"`

	// written are the keys the file wrote, so a field refused by name is
	// refused when written at all, a zero or false value included.
	written map[string]bool
}

// UnmarshalYAML decodes a probe and notes which keys it wrote.
func (p *Probe) UnmarshalYAML(n *yaml.Node) error {
	type plain Probe
	if err := n.Decode((*plain)(p)); err != nil {
		return err
	}
	if n.Kind == yaml.MappingNode {
		p.written = map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			// A node decodes without the document's KnownFields: refuse
			// unknown keys here, as the rest of the file does.
			if !probeKeys[k] {
				return fmt.Errorf("line %d: a health probe has no field %q", n.Content[i].Line, k)
			}
			p.written[k] = true
		}
	}
	return nil
}

// probeKeys are the keys a probe may write (its yaml tags).
var probeKeys = func() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(Probe{})
	for i := range t.NumField() {
		if tag := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]; tag != "" && tag != "-" {
			keys[tag] = true
		}
	}
	return keys
}()

// Health is the per-part `health:` block.
type Health struct {
	Readiness *Probe `yaml:"readiness" json:"readiness,omitempty"`
	Liveness  *Probe `yaml:"liveness" json:"liveness,omitempty"`
}

// validateHealth checks one part's declared health intent. Errors name the part
// and the field, for a human or an agent to fix. The guardrails from the design
// live here so a bad declaration is caught at parse time, not at render.
func validateHealth(part string, h *Health) error {
	if h == nil {
		return nil
	}
	if err := validateProbe(part, "liveness", h.Liveness); err != nil {
		return err
	}
	if err := validateProbe(part, "readiness", h.Readiness); err != nil {
		return err
	}
	return nil
}

func validateProbe(part, which string, p *Probe) error {
	if p == nil {
		return nil
	}
	targets := 0
	if p.HTTP != "" {
		targets++
	}
	if p.TCP != 0 {
		targets++
	}
	if len(p.Exec) > 0 {
		targets++
	}
	if targets == 0 {
		return fmt.Errorf("part %q: health.%s must declare exactly one of http, tcp or exec", part, which)
	}
	if targets > 1 {
		return fmt.Errorf("part %q: health.%s declares more than one of http/tcp/exec — pick one", part, which)
	}
	// Refused rather than accepted and ignored (no target uses them yet), so
	// a target that learns them later changes no app that wrote them.
	if which == "readiness" && p.HTTP == "" {
		return fmt.Errorf("part %q: health.readiness is an http probe (a path); tcp and exec are for liveness", part)
	}
	for _, unused := range []string{"period", "timeout", "failureThreshold", "successThreshold", "checksDeps"} {
		if p.written[unused] {
			return fmt.Errorf("part %q: health.%s.%s is not used by any target yet; remove it", part, which, unused)
		}
	}
	switch p.RequestContext {
	case "", "frontDoor", "direct":
	default:
		return fmt.Errorf("part %q: health.%s.requestContext must be frontDoor or direct, got %q", part, which, p.RequestContext)
	}
	for _, d := range []struct{ name, v string }{
		{"startupGrace", p.StartupGrace}, {"period", p.Period}, {"timeout", p.Timeout},
	} {
		if d.v != "" {
			if _, err := ParseHealthDuration(d.v); err != nil {
				return fmt.Errorf("part %q: health.%s.%s: %w", part, which, d.name, err)
			}
		}
	}
	return nil
}

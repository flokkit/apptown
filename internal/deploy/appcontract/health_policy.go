package appcontract

import (
	"fmt"
	"time"
)

// HealthPolicy is an admitted probe's execution policy. Explicit distinguishes
// newly declared policy from a target's omitted-input behavior. ChecksDeps is
// the application's assertion about readiness, never witnessed dependency health.
// Callers retain this value with their admitted probe rather than reconstructing
// it from current application settings.
type HealthPolicy struct {
	Explicit         bool          `json:"explicit,omitempty"`
	Period           time.Duration `json:"period"`
	Timeout          time.Duration `json:"timeout"`
	StartupGrace     time.Duration `json:"startupGrace,omitempty"`
	FailureThreshold int           `json:"failureThreshold"`
	SuccessThreshold int           `json:"successThreshold"`
	ChecksDeps       bool          `json:"checksDeps,omitempty"`
}

// NormalizeHealthPolicy resolves declared policy over the executing target's
// defaults. Purpose is "readiness" or "liveness". It does not admit a probe
// mode or component: the target must separately check those capabilities.
// Defaults remain target-owned; limits apply to fields the application writes.
func NormalizeHealthPolicy(p *Probe, purpose string, defaults HealthPolicy) (HealthPolicy, error) {
	if purpose != "readiness" && purpose != "liveness" {
		return HealthPolicy{}, fmt.Errorf("unknown health policy purpose %q", purpose)
	}
	out := defaults
	out.Explicit = false
	if p != nil {
		for _, field := range []struct {
			name string
			raw  string
			dst  *time.Duration
			min  time.Duration
			max  time.Duration
		}{
			{"period", p.Period, &out.Period, 5 * time.Second, 300 * time.Second},
			{"timeout", p.Timeout, &out.Timeout, time.Second, 10 * time.Second},
		} {
			if !p.written[field.name] && field.raw == "" {
				continue
			}
			out.Explicit = true
			d, err := ParseHealthDuration(field.raw)
			if err != nil || d < field.min || d > field.max || d%time.Second != 0 {
				return HealthPolicy{}, fmt.Errorf("health.%s.%s must be whole seconds from %s through %s", purpose, field.name, field.min, field.max)
			}
			*field.dst = d
		}
		for _, field := range []struct {
			name  string
			value int
			dst   *int
		}{
			{"failureThreshold", p.FailureThreshold, &out.FailureThreshold},
			{"successThreshold", p.SuccessThreshold, &out.SuccessThreshold},
		} {
			if !p.written[field.name] && field.value == 0 {
				continue
			}
			out.Explicit = true
			if field.value < 1 || field.value > 50 {
				return HealthPolicy{}, fmt.Errorf("health.%s.%s must be from 1 through 50", purpose, field.name)
			}
			*field.dst = field.value
		}
		if p.written["checksDeps"] || p.ChecksDeps {
			out.Explicit = true
			out.ChecksDeps = p.ChecksDeps
		}
		if p.written["startupGrace"] || p.StartupGrace != "" {
			grace, err := ParseHealthDuration(p.StartupGrace)
			if err != nil {
				return HealthPolicy{}, fmt.Errorf("health.%s.startupGrace: %w", purpose, err)
			}
			out.StartupGrace = max(out.StartupGrace, grace)
		}
	}
	if out.Period <= 0 || out.Timeout <= 0 || out.StartupGrace < 0 || out.FailureThreshold < 1 || out.SuccessThreshold < 1 {
		return HealthPolicy{}, fmt.Errorf("health.%s requires positive execution defaults and nonnegative startup grace", purpose)
	}
	if purpose == "liveness" && out.ChecksDeps {
		return HealthPolicy{}, fmt.Errorf("health.liveness.checksDeps cannot be true: dependency failures must not restart the application")
	}
	return out, nil
}

// CheckBootFit refuses an explicit readiness streak which cannot fit the
// existing boot budget. Omitted policy keeps the target's existing settle rule.
// Dividing the available budget avoids overflowing a duration multiplication.
func (p HealthPolicy) CheckBootFit() error {
	if !p.Explicit {
		return nil
	}
	budget := max(90*time.Second, p.StartupGrace)
	if p.Period <= 0 || p.Timeout <= 0 || p.SuccessThreshold < 1 || p.Timeout > budget || int64(p.SuccessThreshold-1) > int64((budget-p.Timeout)/p.Period) {
		return fmt.Errorf("health.readiness success streak does not fit the %s boot budget", budget)
	}
	return nil
}

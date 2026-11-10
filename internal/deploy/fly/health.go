package fly

import (
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// readinessDefaults are Fly's check settings when the contract declares no
// policy: every 10 seconds, 5 seconds to answer, 5 seconds' grace.
func readinessDefaults() appcontract.HealthPolicy {
	return appcontract.HealthPolicy{Period: 10 * time.Second, Timeout: 5 * time.Second, StartupGrace: 5 * time.Second, FailureThreshold: 1, SuccessThreshold: 1}
}

// RefusesHealth says why a contract's health declarations cannot be honoured
// on Fly, or nil. Only what Fly enforces end to end is accepted: an HTTP
// readiness check (the one kind the contract declares) whose period,
// timeout and startup grace map to Fly's interval, timeout and grace
// period. Fly's checks route traffic; they do not restart a Machine, so a
// liveness probe is refused. They count no thresholds and accept any 2xx,
// so an explicit threshold and an exact status are refused. Each is refused
// before any need, app or image copy.
func RefusesHealth(c *appcontract.Contract) error {
	if c == nil {
		return errors.New("a Fly release needs its contract")
	}
	for i := range c.Parts {
		if _, err := partCheck(&c.Parts[i]); err != nil {
			return err
		}
	}
	return nil
}

// partCheck is the Fly check for a part's declared readiness, nil for none,
// or why it cannot be one.
func partCheck(p *appcontract.Part) (*serviceCheck, error) {
	if p.Intent == nil {
		return nil, nil
	}
	if p.Intent.Liveness != nil {
		return nil, fmt.Errorf("part %s declares a liveness probe; Fly does not restart a Machine whose check fails, so this target cannot honour a liveness probe yet", p.Name)
	}
	probe := p.Intent.Readiness
	if probe == nil {
		return nil, nil
	}
	switch {
	case probe.HTTP == "":
		return nil, fmt.Errorf("part %s: Fly checks a part's readiness over HTTP; declare its path", p.Name)
	case probe.FailureThreshold != 0 || probe.SuccessThreshold != 0:
		return nil, fmt.Errorf("part %s: Fly's checks count no thresholds, so a declared failureThreshold or successThreshold cannot be honoured; remove it", p.Name)
	case probe.Expect != 0:
		return nil, fmt.Errorf("part %s: Fly's HTTP checks pass on any 2xx and cannot be held to status %d; remove expect", p.Name, probe.Expect)
	case p.Port <= 0:
		return nil, fmt.Errorf("part %s: a readiness probe on Fly needs the part's port", p.Name)
	}
	policy, err := appcontract.NormalizeHealthPolicy(probe, "readiness", readinessDefaults())
	if err != nil {
		return nil, fmt.Errorf("part %s readiness: %w", p.Name, err)
	}
	return &serviceCheck{Type: "http", Port: p.Port, Path: probe.HTTP, Method: "GET", Protocol: "http",
		Interval: seconds(policy.Period), Timeout: seconds(policy.Timeout), GracePeriod: seconds(policy.StartupGrace)}, nil
}

func seconds(d time.Duration) string {
	s := int64(d / time.Second)
	if d%time.Second != 0 {
		s++
	}
	return fmt.Sprintf("%ds", s)
}

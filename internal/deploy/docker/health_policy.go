package docker

import (
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// RefusesHealth validates every executed component before any Docker effect.
// Sealed copies execute only the public application, retaining init completion.
func RefusesHealth(c *appcontract.Contract) error {
	return refusesHealth(c, false)
}

func refusesHealth(c *appcontract.Contract, publicOnly bool) error {
	if c == nil {
		return nil
	}
	for _, part := range c.Parts {
		if publicOnly && !part.Public {
			continue
		}
		if part.Intent == nil {
			continue
		}
		if !part.Public && (part.Intent.Readiness != nil || part.Intent.Liveness != nil) {
			return fmt.Errorf("Docker part %s: nonpublic structured health is not supported", part.Name)
		}
		if r := part.Intent.Readiness; r != nil {
			if r.HTTP == "" {
				return fmt.Errorf("Docker part %s: readiness requires HTTP until TCP/exec readiness is supported", part.Name)
			}
			if _, err := readinessPolicy(&part); err != nil {
				return err
			}
		}
		if l := part.Intent.Liveness; l != nil {
			if l.TCP != 0 && l.TCP != part.Port {
				return fmt.Errorf("Docker part %s: liveness TCP must use its declared application port", part.Name)
			}
			if _, err := livenessPolicy(&part); err != nil {
				return err
			}
		}
	}
	return nil
}

func readinessPolicy(part *appcontract.Part) (appcontract.HealthPolicy, error) {
	var p *appcontract.Probe
	if part != nil && part.Intent != nil {
		p = part.Intent.Readiness
	}
	policy, err := appcontract.NormalizeHealthPolicy(p, "readiness", appcontract.HealthPolicy{Period: 3 * time.Second, Timeout: 2 * time.Second, FailureThreshold: 1, SuccessThreshold: 2})
	if err == nil {
		err = policy.CheckBootFit()
	}
	return policy, err
}

func livenessPolicy(part *appcontract.Part) (appcontract.HealthPolicy, error) {
	var p *appcontract.Probe
	if part != nil && part.Intent != nil {
		p = part.Intent.Liveness
	}
	return appcontract.NormalizeHealthPolicy(p, "liveness", appcontract.HealthPolicy{Period: HealthTick, Timeout: 2 * time.Second, StartupGrace: HealthGraceFloor, FailureThreshold: HealthFailThreshold, SuccessThreshold: HealthSuccessThreshold})
}

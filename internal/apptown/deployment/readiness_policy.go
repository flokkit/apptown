package deployment

import (
	"errors"
	"time"
)

// readinessStreak derives consecutive observations for one exact runtime.
// A miss always removes positive evidence; thresholds never grant stale health.
func readinessStreak(previous, current readinessEntry) readinessEntry {
	policy := current.observed.ReadinessPolicy
	if policy == nil || !policy.Explicit {
		return current
	}
	unknown := func() { current.observed.Health = "unknown"; current.observed.HealthObservedAt = time.Time{} }
	if current.err != nil || current.observed.RuntimeToken == "" {
		if current.err == nil {
			current.err = errors.New("readiness policy has no captured runtime identity")
		}
		unknown()
		return current
	}
	oldPolicy := previous.observed.ReadinessPolicy
	if previous.err == nil && !current.attempted.Before(previous.attempted) && current.attempted.Sub(previous.attempted) <= readinessFreshFor(previous) && previous.key == current.key && previous.artifact == current.artifact && previous.observed.RuntimeToken == current.observed.RuntimeToken && oldPolicy != nil && *oldPolicy == *policy {
		current.passes, current.fails = previous.passes, previous.fails
	}
	switch current.observed.Health {
	case "healthy":
		current.passes = min(current.passes+1, policy.SuccessThreshold)
		current.fails = 0
		if current.passes < policy.SuccessThreshold {
			unknown()
		}
	case "unhealthy":
		current.fails = min(current.fails+1, policy.FailureThreshold)
		current.passes = 0
		if current.fails < policy.FailureThreshold {
			unknown()
		}
	default:
		current.passes, current.fails = 0, 0
		unknown()
	}
	return current
}

func readinessFreshFor(entry readinessEntry) time.Duration {
	if p := entry.observed.ReadinessPolicy; p != nil && p.Explicit {
		return p.Period + observationStaleAfter
	}
	return observationStaleAfter
}

func readinessDue(entry readinessEntry, now time.Time) bool {
	p := entry.observed.ReadinessPolicy
	period := observationInterval
	if p != nil && p.Explicit {
		period = p.Period
	}
	return entry.attempted.IsZero() || !now.Before(entry.attempted.Add(period))
}

func readinessAllowance(entry readinessEntry) time.Duration {
	if p := entry.observed.ReadinessPolicy; p != nil && p.Explicit {
		return p.Timeout + 5*time.Second
	}
	return 15 * time.Second
}

func (s *Service) nextReadinessWake() time.Duration {
	now := s.now()
	next := observationInterval
	s.observations.mu.RLock()
	defer s.observations.mu.RUnlock()
	for _, e := range s.observations.readiness {
		if p := e.observed.ReadinessPolicy; p != nil && p.Explicit {
			next = min(next, max(time.Second, e.attempted.Add(p.Period).Sub(now)))
		}
	}
	return next
}

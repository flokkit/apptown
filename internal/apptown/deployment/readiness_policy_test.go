package deployment

import (
	"errors"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestReadinessStreakRequiresSameRuntimeAndNeverToleratesFreshPositiveEvidence(t *testing.T) {
	now := time.Unix(1000, 0)
	policy := appcontract.HealthPolicy{Explicit: true, Period: 10 * time.Second, Timeout: 2 * time.Second, FailureThreshold: 2, SuccessThreshold: 2}
	sample := func(runtime, health string) readinessEntry {
		return readinessEntry{key: [32]byte{1}, artifact: "artifact", attempted: now, observed: deploy.Observation{RuntimeToken: runtime, ReadinessPolicy: &policy, Health: health, HealthObservedAt: now}}
	}
	first := readinessStreak(readinessEntry{}, sample("container/start1", "healthy"))
	if first.observed.Health != "unknown" || !first.observed.HealthObservedAt.IsZero() || first.passes != 1 {
		t.Fatalf("one sample supplied two-pass evidence: %+v", first)
	}
	healthy := readinessStreak(first, sample("container/start1", "healthy"))
	if healthy.observed.Health != "healthy" || healthy.observed.HealthObservedAt != now {
		t.Fatal("second same-runtime success did not qualify")
	}
	for _, change := range []string{"replacement", "restart", "generation", "artifact", "policy", "unavailable", "identity-absent"} {
		t.Run(change, func(t *testing.T) {
			next := sample("container/start1", "healthy")
			switch change {
			case "replacement":
				next.observed.RuntimeToken = "another/start1"
			case "restart":
				next.observed.RuntimeToken = "container/start2"
			case "generation":
				next.key = [32]byte{2}
			case "artifact":
				next.artifact = "other"
			case "policy":
				p := policy
				p.Period = 15 * time.Second
				next.observed.ReadinessPolicy = &p
			case "unavailable":
				next.err = errors.New("read unavailable")
			case "identity-absent":
				next.observed.RuntimeToken = ""
			}
			got := readinessStreak(first, next)
			if got.observed.Health == "healthy" || !got.observed.HealthObservedAt.IsZero() {
				t.Fatalf("%s retained positive evidence: %+v", change, got)
			}
		})
	}
	miss := readinessStreak(healthy, sample("container/start1", "unhealthy"))
	if miss.observed.Health != "unknown" || !miss.observed.HealthObservedAt.IsZero() {
		t.Fatal("a tolerated miss retained healthy evidence")
	}
	failed := readinessStreak(miss, sample("container/start1", "unhealthy"))
	if failed.observed.Health != "unhealthy" {
		t.Fatal("second miss failed to classify unhealthy")
	}
	recovering := readinessStreak(failed, sample("container/start1", "healthy"))
	if recovering.observed.Health != "unknown" {
		t.Fatal("one recovery pass bypassed threshold")
	}
}

func TestExplicitReadinessCadenceUsesSampleStartAndBoundedFreshness(t *testing.T) {
	start := time.Unix(1000, 0)
	policy := appcontract.HealthPolicy{Explicit: true, Period: 10 * time.Second, Timeout: 10 * time.Second, FailureThreshold: 2, SuccessThreshold: 2}
	entry := readinessEntry{attempted: start, observed: deploy.Observation{ReadinessPolicy: &policy}}
	for _, tc := range []struct {
		seconds int
		want    bool
	}{{0, false}, {6, false}, {9, false}, {10, true}, {20, true}} {
		if got := readinessDue(entry, start.Add(time.Duration(tc.seconds)*time.Second)); got != tc.want {
			t.Fatalf("due at%d=%v want%v", tc.seconds, got, tc.want)
		}
	}
	if got := readinessAllowance(entry); got != 15*time.Second {
		t.Fatalf("10-second probe allowance=%s", got)
	}
	if got := readinessFreshFor(entry); got != 55*time.Second {
		t.Fatalf("policy freshness=%s", got)
	}
}

func TestMixedReadinessPoliciesKeepOmittedCadence(t *testing.T) {
	start := time.Unix(1000, 0)
	p := appcontract.HealthPolicy{Explicit: true, Period: 5 * time.Second}
	entries := []readinessEntry{{observed: deploy.Observation{ReadinessPolicy: &p}}, {}}
	counts := []int{0, 0}
	for _, second := range []int{0, 5, 10, 15} {
		now := start.Add(time.Duration(second) * time.Second)
		for i := range entries {
			if readinessDue(entries[i], now) {
				counts[i]++
				entries[i].attempted = now
			}
		}
	}
	if counts[0] != 4 || counts[1] != 2 {
		t.Fatalf("mixed fleet samples explicit=%d omitted=%d", counts[0], counts[1])
	}
}

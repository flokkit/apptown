package wow

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/check"
)

// A policy ceiling judges a scan step by its failOn alone, so it must order
// thresholds exactly as a scan applies them (check.FailsAt): a threshold no
// looser than the ceiling fails on every finding the ceiling's would. An
// empty failOn is the strictest, because a scan with none fails on every
// finding, whatever its scanner looks at.
func TestTheCeilingOrdersFailOnAsAScanAppliesIt(t *testing.T) {
	t.Parallel()
	thresholds := append([]string{""}, schema.FailOnValues...)
	severities := []string{"note", "low", "medium", "high", "critical", "unknown"}
	for _, step := range thresholds {
		for _, ceiling := range thresholds {
			if FailOnLooseness(step) > FailOnLooseness(ceiling) {
				continue
			}
			for _, sev := range severities {
				if check.FailsAt(ceiling, sev) && !check.FailsAt(step, sev) {
					t.Errorf("failOn %q is within the ceiling %q but passes a %s finding the ceiling fails", step, ceiling, sev)
				}
			}
		}
	}
	for _, sev := range severities {
		if !check.FailsAt("", sev) {
			t.Errorf("a scan with no failOn passes a %s finding", sev)
		}
	}
}

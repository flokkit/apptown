package verification

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
)

// Observing a tree judges existing debt, even if a comparison was available.
func TestAnAbsoluteScanCountsCarriedFindings(t *testing.T) {
	finding := check.Finding{Fingerprint: "existing", Identifiers: []string{"CVE-2026-1234"}, Severity: "high"}
	base := scanBase{OK: true, Findings: []check.Finding{finding}}
	cfg := scanConfig{Baseline: "none"}
	_, gating, carried := cfg.gate([]conformance.Finding{{Finding: finding}}, nil, base, true)
	if len(gating) != 1 || len(carried) != 0 {
		t.Fatalf("gating=%v carried=%v", gating, carried)
	}
	_, gating, _ = cfg.gate([]conformance.Finding{{Finding: finding}}, []conformance.Disposition{{Fingerprint: "existing", Status: conformance.AcceptedRisk}}, base, true)
	if len(gating) != 0 {
		t.Fatalf("an active exception did not cover the finding: %v", gating)
	}
}

func TestAnAbsoluteScanWithoutAScannerIsAnError(t *testing.T) {
	if got := (scanConfig{Baseline: "none"}).unattachedStatus(); got != workflow.StatusError {
		t.Fatalf("missing scanner: %s", got)
	}
}

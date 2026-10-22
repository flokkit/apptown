package conformance

import (
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestDispositionMatching(t *testing.T) {
	t.Parallel()
	findings := []Finding{
		{Finding: check.Finding{Fingerprint: "vuln|GO-2025-3955|stdlib", Kind: "vuln",
			Identifiers: []string{"GO-2025-3955", "CVE-2025-47910"},
			Package:     &check.PackageRef{Name: "stdlib"}}},
		{Finding: check.Finding{Fingerprint: "vuln|GO-2025-4006|stdlib", Kind: "vuln",
			Identifiers: []string{"GO-2025-4006"}, Package: &check.PackageRef{Name: "stdlib"}}},
		{Finding: check.Finding{Fingerprint: "secret|github-pat|leaky.txt|1", Kind: "secret",
			Identifiers: []string{"github-pat"}}},
	}
	ds := []Disposition{
		{ID: idstest.N(1), Identifier: "CVE-2025-47910", Package: "stdlib", Status: AcceptedRisk}, // matches #1 by alias+pkg
		{ID: idstest.N(2), Fingerprint: "secret|github-pat|leaky.txt|1", Status: FalsePositive},
		{ID: idstest.N(3), Identifier: "GO-2025-4006", Package: "otherpkg", Status: AcceptedRisk}, // wrong pkg — no match
	}
	actionable := AnnotateFindings(findings, ds)
	if len(actionable) != 1 || actionable[0].Fingerprint != "vuln|GO-2025-4006|stdlib" {
		t.Fatalf("actionable: %+v", actionable)
	}
	if findings[0].Disposition == nil || findings[0].Disposition.ID != idstest.N(1) {
		t.Errorf("finding 0 should match disposition 1")
	}
	if findings[2].Disposition == nil || findings[2].Disposition.ID != idstest.N(2) {
		t.Errorf("finding 2 should match disposition 2")
	}
	if actionable := AnnotateFindings(findings, nil); len(actionable) != len(findings) {
		t.Fatalf("revoked exceptions still suppress findings: %+v", actionable)
	}
	for _, f := range findings {
		if f.Disposition != nil {
			t.Fatalf("current projection retained a revoked exception: %+v", f)
		}
	}

}

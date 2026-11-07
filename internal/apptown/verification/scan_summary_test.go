package verification

import (
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/check"
	"strings"
	"testing"
)

func TestScanComparisonDoesNotClaimDependencyChanges(t *testing.T) {
	t.Parallel()
	findings := []conformance.Finding{{Finding: check.Finding{Title: "New advisory", Severity: "high"}}}
	reason := scanFailReason(findings, scanBase{OK: true})
	if strings.Contains(reason, "introduced by this change") || !strings.Contains(reason, "newly published advisories on unchanged dependencies") || !strings.Contains(reason, "New advisory") {
		t.Fatalf("scan summary claims more provenance than its comparison proves: %s", reason)
	}
}

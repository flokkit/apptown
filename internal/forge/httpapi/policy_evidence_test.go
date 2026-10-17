package httpapi

import (
	"maps"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// These are executable contracts, not names of tests to be kept in a document.
// Forge's inventory test reads the keys; this suite executes the bound cases in
// their owning package so the root never needs an exported fixture API. Each
// runs once: a top-level test on its own, a helper through
// TestRegisteredPolicyEvidence.
var policyEvidence = map[string]func(*testing.T){
	"catalogChanges.CatalogApply":       TestCatalogApplyHTTPKeepsProjectMembershipAndCaller,
	"accounts.CreateToken":              TestTokenHTTPKeepsMetadataAndRoleBounds,
	"accounts.ListTokens":               TestTokenHTTPKeepsMetadataAndRoleBounds,
	"accounts.RevokeToken":              TestTokenHTTPKeepsMetadataAndRoleBounds,
	"deployments.CreateDeployment":      TestDeploymentConfigurationHTTP,
	"deployments.DeleteDeployment":      TestDeploymentRemovalChecksAuthorityAndHTTPOutcome,
	"deployments.DeployDeployment":      TestDeploymentConfigurationHTTP,
	"deployments.DeploymentResources":   TestDeploymentResourcesHTTP,
	"deployments.DeploymentDiagnostics": TestDeploymentDiagnosticsHTTP,
	"deployments.DeploymentLog":         TestDeploymentDiagnosticsHTTP,
	"deployments.SetResourceOverride":   TestDeploymentResourcesHTTP,
	"deployments.ClearResourceOverride": TestDeploymentResourcesHTTP,
}

func TestRegisteredPolicyEvidence(t *testing.T) {
	t.Parallel()
	if !runsAsATopLevelTest(TestRegisteredPolicyEvidence) || runsAsATopLevelTest(func(*testing.T) {}) {
		t.Fatal("a skipped scenario would not run at all: the top-level test rule is wrong")
	}
	seen := map[uintptr]bool{}
	for _, contract := range slices.Sorted(maps.Keys(policyEvidence)) {
		check := policyEvidence[contract]
		if check == nil {
			t.Fatalf("%s has no executable evidence", contract)
		}
		id := reflect.ValueOf(check).Pointer()
		if !seen[id] && !runsAsATopLevelTest(check) {
			t.Run(contract, check)
		}
		seen[id] = true
	}
}

// runsAsATopLevelTest says whether go test already runs f as a test of its
// own: a package-level function in a _test.go file whose name follows go
// test's rule (Test, then anything but a lower-case letter). Such a scenario
// runs once in the suite already; running it here too only repeated it. A
// helper or closure is run by nobody else, so it runs here.
func runsAsATopLevelTest(f func(*testing.T)) bool {
	fn := runtime.FuncForPC(reflect.ValueOf(f).Pointer())
	if file, _ := fn.FileLine(fn.Entry()); !strings.HasSuffix(file, "_test.go") {
		return false
	}
	name := fn.Name()
	_, name, _ = strings.Cut(name[strings.LastIndex(name, "/")+1:], ".")
	rest, ok := strings.CutPrefix(name, "Test")
	if !ok || strings.Contains(rest, ".") {
		return false
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return rest == "" || !unicode.IsLower(r)
}

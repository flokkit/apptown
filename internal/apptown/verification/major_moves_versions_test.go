package verification

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/packagefacts"
)

// Reduced from two locks produced by uv lock: the old Python branch moves
// from numpy 1 to 2 while the newer branch already used 2.
func TestAMarkerBranchLeavingAMajorIsAMove(t *testing.T) {
	t.Parallel()
	locks := []string{`version = 1
[[package]]
name = "numpy"
version = "1.26.4"
source = { registry = "https://pypi.org/simple" }
[[package]]
name = "numpy"
version = "2.0.2"
source = { registry = "https://pypi.org/simple" }
[[package]]
name = "review-example"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
 { name = "numpy", version = "1.26.4", source = { registry = "https://pypi.org/simple" }, marker = "python_full_version < '3.10'" },
 { name = "numpy", version = "2.0.2", source = { registry = "https://pypi.org/simple" }, marker = "python_full_version >= '3.10'" },
]
`, `version = 1
[[package]]
name = "numpy"
version = "2.0.2"
source = { registry = "https://pypi.org/simple" }
[[package]]
name = "review-example"
version = "0.1.0"
source = { virtual = "." }
dependencies = [{ name = "numpy" }]
`}
	var deps [2][]packagefacts.Dependency
	for i, lock := range locks {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "uv.lock"), []byte(lock), 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		deps[i], err = packagefacts.Direct(dir)
		if err != nil {
			t.Fatal(err)
		}
	}
	moves := MovesBetween(deps[0], deps[1])
	if len(moves) != 1 || moves[0].FromLine != "1" || moves[0].ToLine != "2" {
		t.Fatalf("numpy 1 -> 2 disappeared: base=%+v candidate=%+v moves=%+v", deps[0], deps[1], moves)
	}
}

func TestAFindingOnAnotherVersionAllowsNothing(t *testing.T) {
	t.Parallel()
	dep := packagefacts.Dependency{Package: packagefacts.Package{Type: packagefacts.NPM, Name: "example"}, Version: "1.7.0"}
	young := packagefacts.Lines{Newest: "2", First: packagefacts.Release{Version: "2.0.0", Published: majorDay("2026-11-01")}, Since: 2}
	supported := packagefacts.Support{Tracked: true, Product: "example", Cycles: []packagefacts.Cycle{{Name: "1", Ends: majorDay("2028-01-01")}}}
	// Another manifest uses 1.8.0, affected by an advisory introduced in 1.8;
	// this dependency on 1.7.0 has no matching finding.
	findings := []check.Finding{{Identifiers: []string{"CVE-example"}, Package: &check.PackageRef{Name: "example", Ecosystem: "npm", Version: "1.8.0", FixedIn: "2.0.1"}}}
	got := decideMajor(majorDay("2026-11-21"), dep, "2", young, nil, supported, nil, findings)
	if got.Allowed {
		t.Fatalf("a finding on another version authorized this move: %s", got.Text())
	}
}

package verification

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/packagefacts"
)

func TestAMarkerMoveStartsFromTheVersionThatLeft(t *testing.T) {
	t.Parallel()
	// One Python branch leaves 1.7.0 for 2.0.1; the other stays on 1.8.0.
	read := func(lower, higher string) []packagefacts.Dependency {
		t.Helper()
		dir := t.TempDir()
		lock := fmt.Sprintf(`version = 1
[[package]]
name = "example"
version = %q
source = { registry = "https://pypi.org/simple" }
[[package]]
name = "example"
version = %q
source = { registry = "https://pypi.org/simple" }
[[package]]
name = "project"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
 { name = "example", version = %q, marker = "python_full_version < '3.10'" },
 { name = "example", version = %q, marker = "python_full_version >= '3.10'" },
]
`, lower, higher, lower, higher)
		if err := os.WriteFile(filepath.Join(dir, "uv.lock"), []byte(lock), 0600); err != nil {
			t.Fatal(err)
		}
		deps, err := packagefacts.Direct(dir)
		if err != nil {
			t.Fatal(err)
		}
		return deps
	}
	moves := MovesBetween(read("1.7.0", "1.8.0"), read("2.0.1", "1.8.0"))
	if len(moves) != 1 {
		t.Fatalf("want one major move, got %+v", moves)
	}
	mv := moves[0]
	if mv.From != "1.7.0" {
		t.Errorf("moved branch was 1.7.0; comparison substituted retained version: %+v", mv)
	}
	young := packagefacts.Lines{Newest: "2", First: packagefacts.Release{Version: "2.0.0", Published: majorDay("2026-11-01")}, Since: 2}
	supported := packagefacts.Support{Tracked: true, Product: "example", Cycles: []packagefacts.Cycle{{Name: "1", Ends: majorDay("2028-01-01")}}}
	findings := []check.Finding{{Identifiers: []string{"CVE-example"}, Package: &check.PackageRef{Name: "example", Ecosystem: "PyPI", Version: "1.8.0", FixedIn: "2.0.1"}}}
	dep := packagefacts.Dependency{Package: mv.Package, Version: mv.From, Manifest: mv.Manifest}
	got := decideMajor(majorDay("2026-11-21"), dep, mv.ToLine, young, nil, supported, nil, findings)
	if got.Allowed {
		t.Fatalf("finding on retained 1.8.0 authorized unaffected 1.7.0 branch: %s", got.Text())
	}
}

package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
)

// A scan reads only what its own run wrote. A report an earlier run left
// that cannot be removed is an error before the scanner runs, and so is an
// image an earlier image scan saved (a run saves the image only where no
// file is, so the old one would be scanned in its place).
//
// Kills: the unchecked removals of stale reports and saved images (fallback
// audit F15).
func TestAScanStopsWhenAnEarlierRunsOutputCannotBeRemoved(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	if _, err := integration.New(f.DB).Upsert(t.Context(), "semgrep", ScannerCapability, "tool",
		`{"image":"semgrep/semgrep:test","command":["semgrep","/work"],"report":".apptown/semgrep.sarif","format":"sarif","target":"source","kind":"sast"}`, true); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, ".apptown", "semgrep.sarif", "kept"), 0o755); err != nil {
		t.Fatal(err)
	}
	results, findings, errs := f.Scanners.RunSourceScans(context.Background(), src, "test", "", "")
	if f.Effects.ran("semgrep/semgrep:test") || errs != 1 || len(findings) != 0 || len(results) != 1 || results[0].Verdict != workflow.VerdictError {
		t.Fatalf("a stale report left in place: ran %v, errors %d, results %+v", f.Effects.ran("semgrep/semgrep:test"), errs, results)
	}

	image := check.Scanner{Name: "trivy", Image: "trivy", Report: ".apptown/trivy.sarif", Target: "image"}
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, ".apptown", "image.tar", "kept"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := clearForScan(tree, image); err == nil {
		t.Fatal("an earlier saved image that cannot be removed was left for the scan")
	}
}

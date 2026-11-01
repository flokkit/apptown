package verification

import (
	"context"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// apptown check reflects the land gate's SOURCE scanners: an attached non-manual
// source scanner runs against the staged tree and its findings come back so the
// agent sees them in-loop. A manual (way-of-working) scanner is NOT fanned out
// here — it only runs when a tool check names it.
func TestRunAttachedSourceScans(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	// A universal source scanner (fanned out) and a manual one (must be skipped).
	f.bind(t, "semgrep", map[string]any{
		"image": "semgrep/semgrep:test", "command": []string{"semgrep", "/work"},
		"report": ".apptown/semgrep.sarif", "network": true, "format": "sarif",
		"kind": "sast", "target": "source",
	})
	f.bind(t, "semgrep-django", map[string]any{
		"image": "semgrep/semgrep:test", "command": []string{"semgrep", "/work"},
		"report": ".apptown/semgrep.sarif", "format": "sarif", "target": "source", "manual": true,
	})

	f.Effects.report = scannerSARIF(2) // writes .apptown/semgrep.sarif with 2 results
	src := projectTree(t)
	results, findings, errs := f.Scanners.runSourceScans(context.Background(), src, "test", "", "")

	if errs != 0 {
		t.Fatalf("unexpected scan errors: %d", errs)
	}
	// Only the universal 'semgrep' ran; the manual 'semgrep-django' was skipped.
	ran := map[string]any{}
	for _, r := range results {
		ran[r.Check] = r.Verdict
	}
	if _, ok := ran["semgrep-django"]; ok {
		t.Fatalf("manual scanner was fanned out — it must run only when named: %+v", ran)
	}
	if ran["semgrep"] != workflow.VerdictFail {
		t.Fatalf("want semgrep fail, got %+v", ran)
	}
	if len(findings) < 1 {
		t.Fatalf("scan findings were not surfaced to the agent, got %d", len(findings))
	}
}

// Scanner discovery the caller stopped found nothing, which is not "no
// scanners": the scans were not assessed, and say so as an errored result
// that keeps apptown check from answering ok.
//
// Kills: an unreadable binding list read as no scanners attached.
func TestStoppedScannerDiscoveryIsNotAPass(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	f.bind(t, "semgrep", map[string]any{
		"image": "semgrep/semgrep:test", "command": []string{"semgrep", "/work"},
		"report": ".apptown/semgrep.sarif", "format": "sarif", "kind": "sast", "target": "source",
	})
	stopped, cancel := context.WithCancel(t.Context())
	cancel()
	results, findings, errs := f.Scanners.runSourceScans(stopped, projectTree(t), "test", "", "")
	if errs == 0 || len(results) != 1 || results[0].Verdict != workflow.VerdictError || len(findings) != 0 {
		t.Fatalf("scans after their caller stopped: errors %d, results %+v, findings %d; want one errored result", errs, results, len(findings))
	}
}

// A scanner whose manifest clears its image's entrypoint (so its command can
// trust the extra certificates first, internal/trust) runs with it cleared,
// its command whole.
func TestAScannerThatClearsItsEntrypointRunsItsCommandWhole(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	command := []string{"sh", "-c", `eval "${APPTOWN_TRUST:-}"; exec "$@"`, "sh", "semgrep", "/work"}
	f.bind(t, "semgrep", map[string]any{
		"image": "semgrep/semgrep:test", "command": command, "noEntrypoint": true,
		"report": ".apptown/semgrep.sarif", "network": true, "format": "sarif", "kind": "sast", "target": "source",
	})
	f.Effects.report = scannerSARIF(0)
	f.Scanners.runSourceScans(context.Background(), projectTree(t), "test", "", "")
	f.Effects.mu.Lock()
	defer f.Effects.mu.Unlock()
	if len(f.Effects.requests) != 1 || !f.Effects.requests[0].NoEntrypoint || !slices.Equal(f.Effects.requests[0].Argv, command) || f.Effects.requests[0].Network != "" {
		t.Fatalf("requests %+v", f.Effects.requests)
	}
}

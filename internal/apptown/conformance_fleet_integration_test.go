//go:build integration

package apptown

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
)

// Integration tier: run the fleet posture review through the REAL production
// path — runShChecks in the pinned, read-only, network-less alpine container
// (no local-shell seam) — proving the container evaluation, not just the
// exit-code logic the DEFAULT-tier tests cover. Run with:
//
//	go test -tags=integration ./internal/apptown/ -run TestIntegrationFleetReview -v
func TestIntegrationFleetReview(t *testing.T) {
	requireDocker(t)
	a := conformanceTestApp(t)

	// A clean project conforms; a project with a FIXME does not — evaluated in
	// the real container against each project's committed SHA.
	good, _, _ := fleetRepo(t, a, "good", map[string]string{"a.txt": "all good here"})
	adopt(t, a, good, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
	bad, _, _ := fleetRepo(t, a, "bad", map[string]string{"b.txt": "// FIXME hurry"})
	adopt(t, a, bad, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))

	rev, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rev.Projects != 2 {
		t.Fatalf("expected 2 projects assessed, got %d", rev.Projects)
	}
	got := map[string]conformance.Outcome{}
	for _, v := range verdicts {
		got[v.ProjectSlug] = v.State
	}
	if got["good"] != conformance.Conforms {
		t.Errorf("good project via real container: %q, want conforms", got["good"])
	}
	if got["bad"] != conformance.DoesNotConform {
		t.Errorf("bad project via real container: %q, want does-not-conform", got["bad"])
	}
	if rev.Errors != 0 {
		t.Errorf("no evaluation should have errored, got %d", rev.Errors)
	}
}

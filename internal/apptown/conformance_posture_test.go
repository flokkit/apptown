package apptown

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/ids"
)

// Fleet review execution: runtime probes, project selection and system attribution.
// Scheduled/import trigger labels record provenance; they do not install a scheduler.

func runtimeCheckJSON(name, probe string) string {
	return `{"appliesTo": ["repository"], "name":"` + name + `","evaluator":"runtime","probe":"` + probe + `","severity":"high","expects":"the rule holds"}`
}

// A runtime expectation is evaluated by a probe against the booted app; with no
// standing deployment (a read-only sweep), it is cannot-assess, never silently passed.
func TestRuntimePostureMode(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	tgLocalSh(t, a)

	id, _, _ := fleetRepo(t, a, "svc", map[string]string{"a.txt": "x"})
	adopt(t, a, id, "health", 1, checksJSON(runtimeCheckJSON("healthz", "GET /healthz")))

	// No standing deployment → the default probe cannot assess this check.
	_, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 1 || verdicts[0].State != conformance.CannotAssess || verdicts[0].Mode != "runtime" {
		t.Fatalf("no-deployment runtime check should be cannot-assess in mode runtime: %+v", verdicts)
	}

	// With a probe wired, the runtime check is really evaluated, and is
	// told the commit the review records.
	var asked string
	installCheckEffects(t, a, checkOverrides{observe: func(_ ids.ID, source, probe string) (conformance.Outcome, string) {
		asked = source
		if probe == "GET /healthz" {
			return conformance.Conforms, ""
		}
		return conformance.DoesNotConform, "no health endpoint"
	}})

	_, verdicts, err = fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 1 || verdicts[0].State != conformance.Conforms {
		t.Fatalf("a wired runtime probe should evaluate the check: %+v", verdicts)
	}
	if asked == "" || asked != verdicts[0].SHA {
		t.Fatalf("the probe was told %q, the review recorded %q", asked, verdicts[0].SHA)
	}
}

// The import gap report evaluates just the newcomer against its adopted ways of
// working, returning how far it is from house standard.
func TestImportGapReport(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	testAdministrator(t, a, "import")
	tgLocalSh(t, a)

	// Two projects exist; only the imported one should be in its gap report.
	other, _, _ := fleetRepo(t, a, "existing", map[string]string{"a.txt": "clean"})
	adopt(t, a, other, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))
	imported, _, _ := fleetRepo(t, a, "newcomer", map[string]string{"a.txt": "// FIXME from outside"})
	adopt(t, a, imported, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))

	rev, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("import"), "import", conformance.ReviewOptions{Projects: []ids.ID{imported}})
	if err != nil {
		t.Fatal(err)
	}
	if rev.Trigger != "import" {
		t.Errorf("gap report should be an import-triggered review, got %q", rev.Trigger)
	}
	if len(verdicts) != 1 || verdicts[0].ProjectSlug != "newcomer" {
		t.Fatalf("gap report must cover ONLY the imported project: %+v", verdicts)
	}
	if verdicts[0].State != conformance.DoesNotConform {
		t.Errorf("the newcomer's gap should show as does-not-conform: %q", verdicts[0].State)
	}
}

// A trigger label is attribution, not system authority.
func TestScheduledTriggerRequiresExplicitAuthority(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	tgLocalSh(t, a)

	id, _, _ := fleetRepo(t, a, "p", map[string]string{"a.txt": "clean"})
	adopt(t, a, id, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))

	countChanges := func() int {
		projects, err := a.Projects.All(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, p := range projects {
			n += len(projectChanges(t, a, p.ID))
		}
		return n
	}
	before := countChanges()

	// The trigger label grants nothing: a person's grant authorizes the run.
	// ("system" is one of AppTown's own names and cannot be a person.)
	if _, _, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("scheduler"), "scheduled", conformance.ReviewOptions{}); err == nil {
		t.Fatal("trigger label granted authority")
	}
	a.addGrant("user", "scheduler", "", string(access.CapConformanceRev), access.ScopeInstance, "", "fixture", "t", "scheduled review fixture")
	rev, _, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("scheduler"), "scheduled", conformance.ReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rev.Trigger != "scheduled" || rev.Actor != "scheduler" {
		t.Fatalf("a scheduled review records its trigger and who ran it: %+v", rev)
	}
	if got, err := savedReview(a, rev.ID); err != nil || got.Trigger != "scheduled" {
		t.Fatalf("the scheduled review must be a saved record: %+v err=%v", got, err)
	}
	if countChanges() != before {
		t.Fatal("a scheduled review must stay read-only — no Change opened")
	}
}

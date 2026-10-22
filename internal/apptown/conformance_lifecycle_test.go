package apptown

import (
	"fmt"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func reviewFixture(t *testing.T) (*App, ids.ID) {
	t.Helper()
	a := conformanceTestApp(t)
	testAdministrator(t, a, "reviewer")
	tgLocalSh(t, a)
	id, _, _ := fleetRepo(t, a, "reviewed", map[string]string{"a.txt": "clean"})
	adopt(t, a, id, "hygiene", 1, checksJSON(shCheck("clean", "exit 0", "high")))
	if _, err := a.conformance.CreateDisposition(testConformanceContext("reviewer"), conformance.ExceptionRequest{ProjectID: id, Fingerprint: "guidance|hygiene@1|clean|", Kind: conformance.AcceptedRisk, Justification: "fixture"}); err != nil {
		t.Fatal(err)
	}
	return a, id
}

func TestReviewStorageFailuresCannotReportSuccess(t *testing.T) {
	t.Parallel()
	for _, fragment := range []string{
		"-- name: ConformanceProjects :many", "-- name: UpdateReviewProgress :execrows", "FROM guidance_imports",
		"-- name: DispositionsForProjects :many", "-- name: InsertReviewException :one", "-- name: LatestVerdictForCheck :one",
		"-- name: InsertVerdict :one", "-- name: FinishReview :execrows",
	} {
		t.Run(fragment, func(t *testing.T) {
			t.Parallel()
			a, id := reviewFixture(t)
			dbtest.FailNextStatement(t, a.DB, fragment)
			rev, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("reviewer"), "manual", conformance.ReviewOptions{Projects: []ids.ID{id}})
			if err == nil || rev == nil {
				t.Fatalf("failed %q must return the created review and an error: %+v %v", fragment, rev, err)
			}
			saved, readErr := savedReview(a, rev.ID)
			if readErr != nil || saved.Status != conformance.ReviewError || saved.FinishedAt.IsZero() || saved.Errors == 0 {
				t.Fatalf("failure must be observable as terminal error: %+v %v", saved, readErr)
			}
			if saved.Verdicts != len(verdicts) {
				t.Fatalf("unwritten verdict counted: %+v %+v", saved, verdicts)
			}
			for _, v := range verdicts {
				if v.ID.IsZero() {
					t.Fatalf("unwritten verdict escaped: %+v", v)
				}
			}
		})
	}
}

// A review someone asks for is queued with its job and runs, when the job
// runs, exactly what was asked: changing the caller's options after
// admission changes nothing. Kills: a queued review reading the caller's
// memory instead of its saved request.
func TestAQueuedReviewRunsWhatWasAsked(t *testing.T) {
	t.Parallel()
	a, id := reviewFixture(t)
	adopt(t, a, id, "runtime", 1, checksJSON(runtimeCheckJSON("ready", "GET /ready")))
	installCheckEffects(t, a, checkOverrides{observe: func(ids.ID, string, string) (conformance.Outcome, string) {
		return conformance.Conforms, "ready"
	}})
	opts := conformance.ReviewOptions{Projects: []ids.ID{id}}
	rev, err := fleetReviewsFor(a).StartFleetReviewWith(testConformanceContext("reviewer"), "manual", opts)
	if err != nil || rev.Status != conformance.ReviewQueued {
		t.Fatalf("admitted: %+v %v", rev, err)
	}
	opts.Projects[0] = idstest.N(999999)
	runQueuedJobForTest(t, a, verification.ReviewJob, fmt.Sprint(rev.ID))
	saved, err := savedReview(a, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != conformance.ReviewDone || saved.ProjectsTotal != 1 || saved.Verdicts != 2 {
		t.Fatalf("queued review's result: %+v", saved)
	}
}

// A queued review's saved build choice reaches its run: asked to build, the
// job builds the app image an exec-image tool needs (here on a worker,
// whose calls are recorded). Kills: the job running with default options
// instead of the saved ones (BQC-01).
func TestAQueuedReviewBuildsWhenItWasAskedTo(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	testAdministrator(t, a, "reviewer")
	attachScanner(t, a, "self-check", map[string]any{"command": []string{"python", "manage.py", "check"}, "target": "exec-image", "manual": true})
	calls := onWorkerWithATripwire(t, a)
	id, _, _ := fleetRepo(t, a, "built", map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n", "Dockerfile": "FROM scratch\n"})
	adopt(t, a, id, "image-checks", 1, `[{"name":"image","evaluator":"tool","tool":"self-check","severity":"high"}]`)
	rev, err := fleetReviewsFor(a).StartFleetReviewWith(testConformanceContext("reviewer"), "manual", conformance.ReviewOptions{Projects: []ids.ID{id}, Build: true})
	if err != nil {
		t.Fatal(err)
	}
	runQueuedJobForTest(t, a, verification.ReviewJob, fmt.Sprint(rev.ID))
	onlyTheWorker(t, calls, "worker build", "python manage.py check")
}

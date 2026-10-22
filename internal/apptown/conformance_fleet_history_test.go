package apptown

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
)

// Conformance (design/subsystems/conformance.md): stale as a projection over
// the immutable records, from a real commit. Saved-review retrieval and the
// worklist states are projections the conformance owner tests on its own
// records (conformance/review_projections_test.go).

// A committed change past a verdict's SHA marks it stale in the current
// projection, and a second review records a NEW verdict without overwriting
// the old one.
func TestFleetStaleAfterCommitAndReReview(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	tgLocalSh(t, a)

	id, sha1, git := fleetRepo(t, a, "alpha", map[string]string{"a.txt": "clean"})
	adopt(t, a, id, "hygiene", 1, checksJSON(shCheck("no-fixme", noFixmeSh, "high")))

	// Review 1 over sha1 → conforms.
	if _, _, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{}); err != nil {
		t.Fatal(err)
	}
	v1, err := savedLatestVerdict(a,

		id, "hygiene", 1, "no-fixme")
	if err != nil || v1.State != conformance.Conforms || v1.SHA != sha1 {
		t.Fatalf("review 1 should record conforms at sha1: %+v", v1)
	}

	// The tree moves (a new commit), WITHOUT a re-review yet.
	git("commit", "-q", "--allow-empty", "-m", "move head")
	head := gitHead(t, a, "alpha")
	if head == sha1 {
		t.Fatal("head should have advanced")
	}

	// Current conformance now overlays STALE — the verdict judged an older SHA.
	cur := currentFor(t, a, id, "no-fixme")
	if cur.State != conformance.Stale {
		t.Fatalf("after the tree moved, current state should be stale, got %q", cur.State)
	}
	// Reading the saved review must not apply the current tree's stale overlay.
	assertSaved := func(reviewID, verdictID ids.ID, sha string) {
		t.Helper()
		verdicts, err := savedVerdicts(a, reviewID)
		if err != nil || len(verdicts) != 1 || verdicts[0].ID != verdictID || verdicts[0].SHA != sha || verdicts[0].State != conformance.Conforms {
			t.Fatalf("saved review %d changed: %+v %v", reviewID, verdicts, err)
		}
	}
	assertSaved(v1.ReviewID, v1.ID, sha1)

	// Re-review records new evidence; both review identities remain retrievable.
	r2, verdicts, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{})
	if err != nil || len(verdicts) != 1 || verdicts[0].ID == v1.ID || r2.ID == v1.ReviewID {
		t.Fatalf("re-review did not produce new evidence: %+v %+v %v", r2, verdicts, err)
	}
	assertSaved(v1.ReviewID, v1.ID, sha1)
	assertSaved(r2.ID, verdicts[0].ID, head)

	if cur := currentFor(t, a, id, "no-fixme"); cur.State != conformance.Conforms || cur.Record.SHA != head {
		t.Fatalf("after re-review current should be fresh at the new head: %+v", cur)
	}
}

// --- helpers ---

func gitHead(t *testing.T, a *App, slug string) string {
	t.Helper()
	sha, _ := gitexec.Run(t.Context(), a.projectRepoPath(slug), "rev-parse", "HEAD")
	return strings.TrimSpace(sha)
}

func currentFor(t *testing.T, a *App, projectID ids.ID, check string) conformance.Observation {
	t.Helper()
	cur, err := a.conformance.
		View(testConformanceContext("t"), ids.Nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range cur.ByProject {
		for _, v := range project {
			if v.ProjectID == projectID && v.Check == check {
				return v
			}
		}
	}
	t.Fatalf("no current verdict for project %d check %q", projectID, check)
	return conformance.Observation{}
}

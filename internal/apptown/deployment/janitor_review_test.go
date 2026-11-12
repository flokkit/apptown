package deployment

import (
	"errors"
	"strings"
	"testing"
)

func TestReviewJanitorHoldNamesTheDeploymentPeopleSee(t *testing.T) {
	w, dep, base, _ := janitorReleaseWorld(t)
	if _, err := w.s.EditDeploymentLabel(t.Context(), w.project, dep.ID, dep.DisplayLabel, "Production"); err != nil {
		t.Fatal(err)
	}
	rel := requestJanitorBundle(t, w, nextJanitorBundle(t, w, base))
	w.s.release.Sources = &requestSources{err: errors.New("cannot prepare")}
	if err := w.s.ExecuteRelease(t.Context(), rel.ID); err == nil {
		t.Fatal("fixture did not fail the release")
	}
	holds, err := w.s.HeldBack(t.Context(), w.project)
	if err != nil || len(holds) != 1 {
		t.Fatalf("holds=%+v: %v", holds, err)
	}
	if holds[0].Deployment != dep.Name || holds[0].DisplayLabel != "Production" {
		t.Fatalf("hold lost stable identity or label: %+v", holds[0])
	}
	if !strings.Contains(holds[0].String(), "Production") {
		t.Fatalf("hold names machine key rather than deployment label: %s", holds[0].String())
	}
}

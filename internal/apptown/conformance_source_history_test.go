package apptown

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/verification"
)

func TestAMissingSourcePreservesHistory(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	tgLocalSh(t, a)
	id, _, _ := fleetRepo(t, a, "before", map[string]string{"a.txt": "clean"})
	adopt(t, a, id, "hygiene", 1, checksJSON(shCheck("clean", noFixmeSh, "high")))
	rev, vs, err := fleetReviewsFor(a).RunFleetReviewWith(testConformanceContext("t"), "manual", conformance.ReviewOptions{})
	if err != nil || len(vs) != 1 {
		t.Fatalf("review: %+v %v", vs, err)
	}
	if err := os.Rename(a.projectRepoPath("before"), filepath.Join(t.TempDir(), "unavailable")); err != nil {
		t.Fatal(err)
	}
	observed := currentFor(t, a, id, "clean")
	if observed.State != conformance.CannotAssess || observed.Record.State != conformance.Conforms || observed.Detail() == observed.Record.Detail {
		t.Fatalf("missing source reported fresh: %+v", observed)
	}
	history, err := a.conformance.
		View(testConformanceContext("t"), rev.ID)
	if err != nil || len(history.ByProject["before"]) != 1 || history.ByProject["before"][0].State != conformance.Conforms {
		t.Fatalf("history was rewritten: %+v %v", history, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verification.NewSource(a.Adoption, a.cfg.dataDir).Head(ctx, conformance.ProjectSource{Slug: "unavailable", Branch: "main"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Git ignored cancellation: %v", err)
	}
}

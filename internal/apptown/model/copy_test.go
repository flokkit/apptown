package model

import (
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestEvalCopyRunsOnlyItsAssignedProfile(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	slots := New(db)
	copyID, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "eval-copy", Name: "Eval copy"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for profile, pattern := range map[string]string{"standard": "cloud/std", "high": "cloud/high"} {
		if err := slots.Set(t.Context(), "review", profile, pattern, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	for _, profile := range []string{"high", "standard"} {
		if err := slots.CopyModels(t.Context(), copyID, profile); err != nil {
			t.Fatal(err)
		}
		p, err := project.ByIDIn(t.Context(), db, copyID)
		if err != nil || p.ModelProfile != profile {
			t.Fatalf("copy profile: %q %v", p.ModelProfile, err)
		}
		pattern, slot, decided, err := NewSelection(slots).Resolve(t.Context(), copyID, "review")
		want := "cloud/std"
		if profile == "high" {
			want = "cloud/high"
		}
		if err != nil || pattern != want || slot != "review" || decided != profile {
			t.Fatalf("copy resolution: %s %s %s %v", pattern, slot, decided, err)
		}
	}
	cells, err := slots.Cells(t.Context())
	if err != nil || len(cells) != 1 || len(cells["review"]) != 2 {
		t.Fatalf("copy changed cells: %v %v", cells, err)
	}
}

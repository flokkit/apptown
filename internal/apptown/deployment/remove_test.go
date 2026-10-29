package deployment

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/database"
)

// The participant removes one project's releases, bundles and deployments
// and nothing of another project's.
func TestRemoveProjectInKeepsOtherProjects(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	a := createTestProject(t, db, "a", "A")
	b := createTestProject(t, db, "b", "B")
	for _, pid := range []ids.ID{a, b} {
		depID := storedDeploymentFixture(t, db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: pid, Name: "main"})
		if _, err := NewReleases(db).Start(context.Background(), ReleaseDraft{Source: ReleaseFromBranch, ProjectID: pid, DeploymentID: depID, Deployment: "main"}, "started"); err != nil {
			t.Fatal(err)
		}
		cutBundleFixture(t, db, pid, "abc", BundleOrigin{})
	}
	if err := db.InTx(context.Background(), func(tx *database.Tx) error { return RemoveProjectIn(context.Background(), tx, a) }); err != nil {
		t.Fatal(err)
	}
	held := map[string]func(pid ids.ID) int{
		"releases": func(pid ids.ID) int { return len(projectReleases(t, db, pid)) },
		"bundles": func(pid ids.ID) int {
			var n int
			// sql-guarantee: b must keep exactly one bundle, not merely a newest one; no owner read lists a project's bundles.
			if err := db.SQL().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM release_bundles WHERE project_id=$1`, pid).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		},
		"deployments": func(pid ids.ID) int {
			envs, err := NewDeployments(db).ForProject(t.Context(), pid)
			if err != nil {
				t.Fatal(err)
			}
			return len(envs)
		},
		"release events": func(pid ids.ID) int {
			n := 0
			for _, r := range projectReleases(t, db, pid) {
				n += len(releaseEvents(t, db, r.ID))
			}
			return n
		},
	}
	for what, count := range held {
		if count(a) != 0 || count(b) != 1 {
			t.Fatalf("%s: a=%d b=%d", what, count(a), count(b))
		}
	}
}

func TestReleaseRetirementInventoryHasNoDisplayLimitOrStateFilter(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	own := createTestProject(t, db, "inventory", "Inventory")
	other := createTestProject(t, db, "neighbor", "Neighbor")
	for _, pid := range []ids.ID{own, other} {
		dep := storedDeploymentFixture(t, db, deploymentsql.EnsureStandingParams{ID: ids.New(time.Now()), ProjectID: pid, Name: "main"})
		count := 105
		if pid == other {
			count = 1
		}
		for i := 0; i < count; i++ {
			rel, err := NewReleases(db).Start(t.Context(), ReleaseDraft{Source: ReleaseFromBranch, ProjectID: pid, DeploymentID: dep, Deployment: "main"}, "inventory fixture")
			if err != nil {
				t.Fatal(err)
			}
			if i < count-1 {
				if _, err := NewOutcomes(db, nil).FailUnstartedRelease(t.Context(), rel.ID, fmt.Errorf("partial release")); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	err := db.InTx(t.Context(), func(tx *database.Tx) error {
		numbers, err := ReleaseNumbersIn(t.Context(), tx, own)
		if err != nil {
			return err
		}
		if len(numbers) != 105 {
			t.Fatalf("truncated inventory: %d", len(numbers))
		}
		for i, n := range numbers {
			if n != int64(i+1) {
				t.Fatalf("inventory omitted/reordered number: %v", numbers)
			}
		}
		neighbor, err := ReleaseNumbersIn(t.Context(), tx, other)
		if err != nil {
			return err
		}
		if len(neighbor) != 1 || neighbor[0] != 1 {
			t.Fatalf("other ownership: %v", neighbor)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

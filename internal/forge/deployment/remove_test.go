package deployment

import (
	"context"
	"testing"
	"time"

	"flokkit.io/forge/internal/forge/deployment/internal/deploymentsql"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
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
			if err := db.QueryRow(`SELECT COUNT(*) FROM release_bundles WHERE project_id=?`, pid).Scan(&n); err != nil {
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

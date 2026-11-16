package verification

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
)

func TestStandingReachReadsRepositoryWithoutReadingGit(t *testing.T) {
	t.Parallel()
	f := newScannerOwnerFixture(t)
	projects := project.New(f.DB, nil)
	// A repository-backed record without any on-disk repository: reach must
	// not inspect the contract or main's scan observations.
	if err := projects.SetDefaultBranch(t.Context(), f.ProjectID, "main"); err != nil {
		t.Fatal(err)
	}
	portfolio := Portfolio{}
	reach, err := portfolio.StandingReach(t.Context(), f.DB, f.ProjectID)
	if err != nil || !reach.Repository {
		t.Fatalf("reach: %+v %v", reach, err)
	}
	if err := projects.SetDefaultBranch(t.Context(), f.ProjectID, ""); err != nil {
		t.Fatal(err)
	}
	reach, err = portfolio.StandingReach(t.Context(), f.DB, f.ProjectID)
	if err != nil || reach.Repository {
		t.Fatalf("no repository: %+v %v", reach, err)
	}
}

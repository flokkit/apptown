package deployment

import (
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/operations"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"testing"
)

func TestRuntimeListingConstantQueryContract(t *testing.T) {
	s, db, _, _, target := healthWorld(t)
	s.operations = operations.New(db, &requestAuthority{}, &requestAuthority{}, s.now)
	visible := []ids.ID{}
	auth := &listingWorld{}
	s.authority = auth
	counts := []int{}
	for i := 0; i < 8; i++ {
		id := createTestProject(t, db, fmt.Sprintf("q-%d", i), "Query project")
		visible = append(visible, id)
		dep, err := s.Deployments.EnsureStanding(t.Context(), id, "main", target.ID, Attributes{}.WithDefaults())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Deployments.ReserveHostPort(t.Context(), id, "main", fmt.Sprintf("query-%d", i), 42000+i); err != nil {
			t.Fatal(err)
		}
		attempt, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: fmt.Sprintf("query-%d", i), Inputs: Inputs{Applied: AppliedProfile{Parts: map[string]AppliedPart{"web": {}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Rollout: "active"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Deployments.OwnerOf(dep).Created(t.Context(), deploy.Owned{Kind: "image", ID: fmt.Sprintf("sha256:query-%d", i)}); err != nil {
			t.Fatal(err)
		}
		if i != 1 && i != 7 {
			continue
		}
		auth.projects = visible
		counted := &countedQueries{}
		stop := db.ObserveStatements("", func([]any) { counted.reads++ })
		_, err = s.ProjectRuntime(t.Context(), visible)
		stop()
		if err != nil {
			t.Fatal(err)
		}
		counts = append(counts, counted.reads)
	}
	t.Logf("2 and 8 projects: %v queries", counts)
	if counts[0] != counts[1] {
		t.Fatalf("2 and 8 projects: %v queries", counts)
	}
}

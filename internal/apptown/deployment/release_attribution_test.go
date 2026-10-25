package deployment

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// Historical releases retain their names, but only a recorded deployment
// identity can establish liveness or receive that deployment's teardown event.
func TestReleaseAttributionDoesNotFollowReusedDeploymentNames(t *testing.T) {
	for _, historicalID := range []ids.ID{ids.Nil, idstest.N(998)} {
		t.Run(historicalID.String(), func(t *testing.T) {
			t.Parallel()
			db, releases, draft := releaseCreationFixture(t, ReleaseFromBundle)
			cut, err := releases.Start(t.Context(), draft, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			deployments := NewDeployments(db)
			dep, err := deployments.ByID(t.Context(), draft.DeploymentID)
			if err != nil {
				t.Fatal(err)
			}
			outcomes := NewOutcomes(db, nil)
			attempt, _, err := outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "release", Inputs: Inputs{ReleaseID: cut.ID, BundleID: draft.BundleID, Revision: draft.Revision}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := outcomes.Complete(t.Context(), Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Rollout: "active"}}); err != nil {
				t.Fatal(err)
			}
			current, err := releases.ByNumber(t.Context(), draft.ProjectID, cut.Number)
			if err != nil {
				t.Fatal(err)
			}
			if live, err := releases.Live(t.Context(), current); err != nil || !live {
				t.Fatalf("bound release: live=%v err=%v", live, err)
			}
			// sql-guarantee: retained history can be unbound or name a removed deployment; current release admission cannot manufacture that historical provenance.
			oldID := mustInsert(t, db, `INSERT INTO releases(id,project_id,number,state,deployment,deployment_id,created_at) VALUES (?,?,?,'released',?,?,'2026-01-04')`, draft.ProjectID, cut.Number+1, dep.Name, historicalID)
			historical := storedRelease(t, db, draft.ProjectID, oldID)
			if live, err := releases.Live(t.Context(), historical); err != nil || live {
				t.Errorf("historical name is not liveness: live=%v err=%v", live, err)
			}
			if live, err := releases.Live(t.Context(), current); err != nil || !live {
				t.Errorf("other identity displaced current release: live=%v err=%v", live, err)
			}
			prior, err := releases.Events(t.Context(), oldID)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.InTx(t.Context(), func(tx *database.Tx) error {
				return takeDownReleaseIn(context.Background(), tx, dep, "target", "alice", time.Now())
			}); err != nil {
				t.Fatal(err)
			}
			events, err := releases.Events(t.Context(), cut.ID)
			if err != nil || len(events) == 0 || events[len(events)-1].Text != "taken down — alice deleted the deployment main on target; it no longer serves this release" {
				t.Fatalf("bound teardown event: %+v %v", events, err)
			}
			after, err := releases.Events(t.Context(), oldID)
			if err != nil || !reflect.DeepEqual(prior, after) {
				t.Fatalf("teardown altered historical events: %+v %v", after, err)
			}
			if after := storedRelease(t, db, draft.ProjectID, oldID); !reflect.DeepEqual(historical, after) {
				t.Fatalf("historical release rewritten: %+v", after)
			}
		})
	}
}

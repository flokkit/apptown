package deployment

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

type countedDeclarations struct{ calls map[ids.ID]int }

func (d countedDeclarations) Declared(_ context.Context, id ids.ID) (Declared, bool) {
	d.calls[id]++
	return Declared{KeepsData: true, Needs: []string{"postgres"}}, true
}

func TestInventoryBatchKeepsDockerAndCloudClaimsAndReadsDeclarationsOnce(t *testing.T) {
	t.Parallel()
	s, db, _, projectID, target := healthWorld(t)
	declarations := countedDeclarations{calls: map[ids.ID]int{}}
	s.Inventory.declared = declarations
	cloud, err := s.Targets.Put(t.Context(), &Target{Name: "cloud-batch", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	targets := []*Target{target, cloud}
	for i, target := range []*Target{target, target, cloud} {
		name := fmt.Sprintf("batch-%d", i)
		dep, err := s.Deployments.EnsureStanding(t.Context(), projectID, name, target.ID, Attributes{}.WithDefaults())
		if err != nil {
			t.Fatal(err)
		}
		if target.Kind == KindDockerHost {
			if _, err := s.Deployments.ReserveHostPort(t.Context(), projectID, name, "batch-"+name, 42000+i); err != nil {
				t.Fatal(err)
			}
		} else {
			attempt, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: name, Inputs: Inputs{Applied: AppliedProfile{Parts: map[string]AppliedPart{"web": {}}}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Outcomes.Complete(t.Context(), Completion{Attempt: attempt, Observed: deploy.Observation{Generation: attempt.Generation, Handle: map[string]any{"appId": "cloud-app", "appName": "cloud app"}, Rollout: "active"}}); err != nil {
				t.Fatal(err)
			}
			if err := s.needs.recordFulfilled(t.Context(), dep.ID, "postgres", nil, map[string]any{"id": "database", "name": "database"}); err != nil {
				t.Fatal(err)
			}
		}
		if target.Kind == KindDOApp {
			if err := db.InTx(t.Context(), func(tx *database.Tx) error {
				_, err := retainTakenDownIn(t.Context(), tx, dep, "fixture", []deploy.Left{{What: "database", Data: true, Handle: map[string]any{"kind": "database", "id": "retained-db", "name": "retained-db"}}}, s.now())
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Deployments.OwnerOf(dep).Created(t.Context(), deploy.Owned{Kind: "volume", ID: name}); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := s.Inventory.RecordedTargets(t.Context(), targets)
	if err != nil {
		t.Fatal(err)
	}
	if declarations.calls[projectID] != 1 {
		t.Fatalf("declarations read %d times", declarations.calls[projectID])
	}
	if len(batch[target.ID]) != 8 || len(batch[cloud.ID]) != 3 {
		t.Fatalf("batch claims: %+v", batch)
	}
	if retained := batch[cloud.ID][2]; retained.Retained == nil || retained.Retained.Project != "p" || retained.Name != "retained-db" {
		t.Fatalf("retained claim lost: %+v", retained)
	}
	for _, target := range targets {
		single, err := s.Inventory.Recorded(t.Context(), target)
		if err != nil || !reflect.DeepEqual(single, batch[target.ID]) {
			t.Fatalf("%s parity: %+v != %+v (%v)", target.Name, single, batch[target.ID], err)
		}
		// A complete empty inventory witnesses precisely the same missing claims.
		if !reflect.DeepEqual(MergeInventory(single, nil), MergeInventory(batch[target.ID], nil)) {
			t.Fatal("missing claims changed")
		}
	}
}

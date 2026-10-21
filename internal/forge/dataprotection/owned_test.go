package dataprotection

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/forge/deployment"
)

// What a driver records creating is the list take-down is handed, once per
// thing, and it goes with the deployment's record.
func TestTakeDownIsHandedWhatTheDeploymentRecordedCreating(t *testing.T) {
	t.Parallel()
	s, _, driver, dep := removalFixture(t)
	ctx := context.Background()
	owner := s.Deployments.OwnerOf(dep)
	made := []deploy.Owned{
		{Kind: "container", ID: "c1", Name: "apptown-env-shop-staging"},
		{Kind: "volume", ID: "apptown-data-apptown-env-shop-staging", Data: true},
		{Kind: "volume", ID: "v-pg", Data: true, Need: "postgres"},
	}
	for _, o := range append(made, made[0]) {
		if err := owner.Created(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := owner.Created(ctx, deploy.Owned{Kind: "network"}); err == nil {
		t.Fatal("an entry without an identity was recorded")
	}
	got, err := s.Deployments.Owned(ctx, dep.ID)
	if err != nil || !reflect.DeepEqual(got, made) {
		t.Fatalf("owned: %+v %v", got, err)
	}
	if _, err := s.Remove(ctx, removeSelection(dep)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(driver.got.Owned, made) {
		t.Fatalf("teardown was handed %+v", driver.got.Owned)
	}
	if got, err := s.Deployments.Owned(ctx, dep.ID); err != nil || len(got) != 0 {
		t.Fatalf("the list outlived its deployment: %+v %v", got, err)
	}
}

// What a teardown deleted is in the result and the receipt; what it left
// (data kept, a delete that failed) becomes a retained row with its reason,
// marked when it holds data.
func TestTakeDownRecordsWhatWasDeletedAndWhatWasLeft(t *testing.T) {
	t.Parallel()
	s, _, driver, dep := removalFixture(t)
	driver.taken = deploy.TakenDown{
		Deleted: []string{"container apptown-env-shop-staging", "image reg/shop@sha256:aa"},
		Left: []deploy.Left{
			{What: "volume anon-pg", Data: true, Handle: map[string]any{"kind": "volume", "name": "anon-pg"}, Why: "kept: no final backup covers it"},
			{What: "network apptown-net-env-shop-staging", Handle: map[string]any{"kind": "network"}, Why: "docker network rm: refused"},
		},
	}
	result, err := s.Remove(context.Background(), removeSelection(dep))
	if err != nil || !reflect.DeepEqual(result.Deleted, driver.taken.Deleted) {
		t.Fatalf("removal: %+v %v", result, err)
	}
	rows, err := s.Retained.ForProject(context.Background(), dep.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]deployment.RetainedResource{}
	for _, r := range rows {
		got[r.Need] = r
	}
	if r := got["volume anon-pg"]; !r.HoldsData || r.Handle["why"] != "kept: no final backup covers it" || r.Disposition != deployment.RetainedOpen || r.Deployment != dep.Name {
		t.Fatalf("kept data: %+v", r)
	}
	if r := got["network apptown-net-env-shop-staging"]; r.HoldsData || r.Handle["why"] != "docker network rm: refused" {
		t.Fatalf("failed delete: %+v", r)
	}
	if r := got["postgres"]; !r.HoldsData {
		t.Fatalf("a managed database holds data: %+v", r)
	}
}

// Data an earlier deployment left at its target holds the deployment's name
// there, whether it was kept (no backup on the target) or its delete
// failed; settling it released frees the name. What holds no data never
// holds a name.
func TestDataLeftAtATargetHoldsTheNameUntilReleased(t *testing.T) {
	t.Parallel()
	s, _, driver, dep := removalFixture(t)
	driver.taken = deploy.TakenDown{Left: []deploy.Left{{What: "network n", Handle: map[string]any{"kind": "network"}, Why: "refused"}}}
	ctx := context.Background()
	if _, err := s.Remove(ctx, removeSelection(dep)); err != nil {
		t.Fatal(err)
	}
	target, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	again := deployment.ConfigureDeployment{ProjectID: dep.ProjectID, Name: dep.Name, Target: target.Name}
	if _, err := s.Configure(ctx, again); !errors.Is(err, deployment.ErrNameHeldByData) || !strings.Contains(err.Error(), "postgres") || strings.Contains(err.Error(), "network") {
		t.Fatalf("the name was given while its data stands: %v", err)
	}
	rows, err := s.Retained.ForProject(ctx, dep.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.HoldsData {
			if err := s.SettleRetained(ctx, deployment.SettleRetained{ProjectID: dep.ProjectID, ResourceID: r.ID, Disposition: deployment.RetainedReleased, Note: "deleted at DigitalOcean"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.Configure(ctx, again); err != nil {
		t.Fatalf("released data still holds the name: %v", err)
	}
}

// A deployment is the last user of what the project holds on an account
// when no other deployment of the project is on a target of the same kind
// with the same connector, whichever target that is; a deployment on
// another account does not count.
func TestTheLastDeploymentOnAnAccountIsTheLastUser(t *testing.T) {
	t.Parallel()
	s, _, driver, dep := removalFixture(t)
	ctx := context.Background()
	put := func(name, provider string) *deployment.Target {
		t.Helper()
		target, err := s.Targets.Put(ctx, &deployment.Target{Name: name, Kind: deployment.KindDOApp, Provider: provider, Origin: deployment.OriginAttached, Status: deployment.TargetReady, NeedsMode: "managed"})
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	cloud, err := s.Targets.ByID(ctx, dep.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	other := put("cloud-mel", cloud.Provider)
	elsewhere := put("cloud-other-account", cloud.Provider+"-other")
	second, err := s.Deployments.EnsureStanding(ctx, dep.ProjectID, "mel", other.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.EnsureStanding(ctx, dep.ProjectID, "away", elsewhere.ID, deployment.Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, removeSelection(dep)); err != nil || driver.got.LastUser {
		t.Fatalf("first of two on the account: last=%v %v", driver.got.LastUser, err)
	}
	if _, err := s.Remove(ctx, removeSelection(second)); err != nil || !driver.got.LastUser {
		t.Fatalf("the last on the account: last=%v %v", driver.got.LastUser, err)
	}
}

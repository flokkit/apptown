package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// A bucket a driver creates is the deployment's from the moment it is
// recorded, before anything is written to it: recording it again is no
// change, and another bucket in its place is refused.
func TestADeploymentRecordsTheBucketItOwns(t *testing.T) {
	t.Parallel()
	s, _, _, projectID, target := healthWorld(t)
	dep, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.RecordOwned(context.Background(), dep.ID, "bucket", "forge-p-main-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.RecordOwned(context.Background(), dep.ID, "bucket", "forge-p-main-1"); err != nil {
		t.Fatalf("recording the same bucket again: %v", err)
	}
	if err := s.Deployments.RecordOwned(context.Background(), dep.ID, "bucket", "forge-p-main-2"); !errors.Is(err, ErrOwnsAnother) {
		t.Fatalf("another bucket in its place: %v", err)
	}
	got, err := s.Deployments.ByID(context.Background(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Handle["bucket"] != "forge-p-main-1" {
		t.Fatalf("handle = %v", got.Handle)
	}
}

// A bucket target serves a static-only app and refuses anything else with
// a sentence.
func TestABucketTargetServesOnlyAStaticOnlyApp(t *testing.T) {
	bucket := &Target{Name: "spaces", Kind: KindS3Bucket, NeedsMode: "managed"}
	static := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Static: "dist"}}}
	if why := bucket.RefusesContract(static); why != "" {
		t.Fatalf("a static-only app: %s", why)
	}
	for name, c := range map[string]*appcontract.Contract{
		"a server":      {Parts: []appcontract.Part{{Name: "web", Public: true, Image: "app:1"}}},
		"a second part": {Parts: []appcontract.Part{{Name: "web", Public: true, Static: "dist"}, {Name: "api", Image: "api:1"}}},
		"init steps":    {Parts: static.Parts, Init: []appcontract.InitContainer{{Name: "seed", Image: "seed:1"}}},
		"needs":         {Parts: static.Parts, Needs: []string{"postgres"}},
	} {
		if why := bucket.RefusesContract(c); !strings.Contains(why, "static-only") {
			t.Fatalf("%s: %q", name, why)
		}
	}
}

// A bucket publishes from the release image in the project's registry: a
// bundle of image IDs (or none) is refused before anything is made.
func TestABucketTargetNeedsAReleaseByDigest(t *testing.T) {
	s := &Service{}
	bucket := &Target{Name: "spaces", Kind: KindS3Bucket}
	if err := s.providers.RefusesBundle(t.Context(), &ReleaseBundle{Parts: map[string]string{"web": "sha256:abc"}}, bucket); err == nil || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("a bundle of image IDs: %v", err)
	}
	if err := s.providers.RefusesBundle(t.Context(), nil, bucket); err == nil {
		t.Fatal("no bundle")
	}
}

// What stands on a bucket target is each deployment's recorded bucket.
func TestABucketTargetHoldsEachDeploymentsBucket(t *testing.T) {
	t.Parallel()
	s, _, _, projectID, _ := healthWorld(t)
	target, err := s.Targets.Put(context.Background(), &Target{Name: "spaces", Kind: KindS3Bucket, Origin: OriginAttached, Address: map[string]any{"endpoint": "https://syd1.example"}, Ingress: map[string]any{}, NeedsMode: "managed", Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.RecordOwned(context.Background(), dep.ID, "bucket", "forge-p-main-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Inventory.Recorded(context.Background(), target)
	if err != nil || len(got) != 1 || got[0].Kind != "bucket" || got[0].Name != "forge-p-main-1" || got[0].Hosts[0].Deployment != "main" {
		t.Fatalf("recorded = %+v", got)
	}
}

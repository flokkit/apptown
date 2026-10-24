package deployment

import (
	"context"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A Docker host's inventory asks for exactly the images the deployments on
// it recorded, and nothing else they own.
//
// Kills: an inventory not told the recorded images (every recorded image
// would read missing), or one told another target's.
func TestADockerHostsInventoryAsksForTheImagesItsDeploymentsRecorded(t *testing.T) {
	t.Parallel()
	s, _, _, projectID, target := healthWorld(t)
	dep, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	own := s.Deployments.OwnerOf(dep)
	for _, o := range []deploy.Owned{{Kind: "image", ID: "reg/app@sha256:aaa"}, {Kind: "image", ID: docker.Current.BuildTag("p-main-g1-web")}, {Kind: "volume", ID: "apptown-data-p"}} {
		if err := own.Created(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	other, err := s.Targets.Put(context.Background(), &Target{Name: "elsewhere", Kind: KindDockerHost, Origin: OriginAttached, Address: map[string]any{"host": "ssh://elsewhere"}, Ingress: map[string]any{}, NeedsMode: "managed", SecretsDelivery: "none", Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := s.Deployments.EnsureStanding(t.Context(), projectID, "other", other.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.OwnerOf(elsewhere).Created(context.Background(), deploy.Owned{Kind: "image", ID: "reg/other@sha256:bbb"}); err != nil {
		t.Fatal(err)
	}

	inv, err := s.providers.TargetInventory(target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := inv.(docker.Inventory).Images(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{docker.Current.BuildTag("p-main-g1-web"), "reg/app@sha256:aaa"}; !slices.Equal(got, want) {
		t.Fatalf("asks for %v, want %v", got, want)
	}
}

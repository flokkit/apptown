package deployment

import (
	"context"
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
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
	other, err := s.Targets.Put(context.Background(), &Target{Name: "elsewhere", Kind: KindDockerHost, Origin: OriginAttached, Address: map[string]any{"host": "ssh://elsewhere"}, Ingress: map[string]any{}, NeedsMode: "managed", Status: TargetReady})
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

// A preview's boot owns its names. A deployment key must never invent a
// second need container or data volume alongside that recorded stack.
func TestPreviewInventoryMatchesTheResourcesItsBootCreated(t *testing.T) {
	t.Parallel()
	s, _, daemon, projectID, target := healthWorld(t)
	s.Inventory.declared = inventoryDeclaration{Declared{KeepsData: true, Needs: []string{"postgres"}}}
	change := idstest.N(7)
	app := docker.PreviewChangeName(change)
	stack := docker.ChangeStackID(change)
	created := []deploy.Owned{
		{Kind: "container", ID: "app-id", Name: app},
		{Kind: "container", ID: "postgres-id", Name: docker.Current.Service(stack, "postgres"), Need: "postgres"},
		{Kind: "network", ID: docker.Current.Network(stack)},
		{Kind: "volume", ID: docker.Current.DataVolume(app), Data: true},
	}
	if err := s.Deployments.RecordPreview(t.Context(), projectID, change, target.ID, app, 40001, "http://preview", created); err != nil {
		t.Fatal(err)
	}
	// Labelled listings, including networks and volumes: confirms-only
	// resources would hide the unrecorded half of the defect.
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		switch {
		case args[0] == "ps":
			return []byte(app + "\tUp\tapp:1\t" + stack + "\t\tinstallation\n" + docker.Current.Service(stack, "postgres") + "\tUp\tpostgres:17\t" + stack + "\t\tinstallation\n"), nil, true
		case len(args) > 1 && args[1] == "ls" && args[0] == "network":
			return []byte(docker.Current.Network(stack) + "\t" + stack + "\t\tinstallation\n"), nil, true
		case len(args) > 1 && args[1] == "ls" && args[0] == "volume":
			return []byte(docker.Current.DataVolume(app) + "\t" + app + "\t\tinstallation\n"), nil, true
		}
		return nil, nil, false
	})
	live, err := (docker.Inventory{Scope: func(context.Context) (docker.Scope, error) {
		return docker.Scope{Client: daemon, Instance: "installation"}, nil
	}}).Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := s.Inventory.Recorded(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	merged := MergeInventory(recorded, live)
	if len(merged) != len(created) {
		t.Errorf("inventory has %d resources, want %d: %+v", len(merged), len(created), merged)
	}
	for _, r := range merged {
		if r.Record != deploy.RecordRecorded {
			t.Errorf("%s %s reads %s", r.Kind, r.Name, r.Record)
		}
	}
}

type inventoryDeclaration struct{ declaration Declared }

func (d inventoryDeclaration) Declared(context.Context, ids.ID) (Declared, bool) {
	return d.declaration, true
}

func TestStandingInventoryKeepsDeploymentKeyNames(t *testing.T) {
	t.Parallel()
	s, _, _, projectID, target := healthWorld(t)
	s.Inventory.declared = inventoryDeclaration{Declared{KeepsData: true, Needs: []string{"postgres"}}}
	dep, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	key := deploy.HostKey(dep.ID)
	// A deploy reserving its port names the container it boots.
	if _, err := s.Deployments.ReserveHostPort(t.Context(), projectID, "main", "apptown-dep-"+key, 18080); err != nil {
		t.Fatal(err)
	}
	got, err := s.Inventory.Recorded(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"container:apptown-dep-" + key:                   true,
		"container:apptown-svc-dep-" + key + "-postgres": true,
		"volume:apptown-data-apptown-dep-" + key:         true,
	}
	for _, r := range got {
		if !want[r.Kind+":"+r.Name] {
			t.Errorf("unexpected standing resource: %+v", r)
		}
		delete(want, r.Kind+":"+r.Name)
	}
	if len(want) > 0 {
		t.Fatalf("missing standing resources: %v", want)
	}
}

// A standing deployment that never booted (a new project bound to the
// built-in host) has nothing on the host: its names are not recorded
// resources, so the host's inventory reads none of them as missing.
func TestStandingInventoryRecordsNothingForANeverBootedDeployment(t *testing.T) {
	t.Parallel()
	s, _, _, projectID, target := healthWorld(t)
	s.Inventory.declared = inventoryDeclaration{Declared{KeepsData: true, Needs: []string{"postgres"}}}
	if _, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Inventory.Recorded(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a never-booted deployment records %+v", got)
	}
}

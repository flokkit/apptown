package dataprotection

import (
	"context"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/deploy/portainer"
	"flokkit.io/forge/internal/forge/deployment"
)

// What a Portainer deployment holds is the volumes its deploys made as well
// as those its stack declares now: a volume an earlier release made and a
// later release's stack dropped is still held, copied as files, and it is
// the deployment's (listed); a volume the stack declares that the deploy did
// not make is held too, and is not.
func TestAPortainerDeploymentHoldsWhatItsDeploysMadeAndItsStackDeclares(t *testing.T) {
	t.Parallel()
	s, _, _, _, pid := backupWorld(t, "pl", answeringPort(t))
	ctx := context.Background()
	target, err := s.Targets.Put(ctx, &deployment.Target{Name: "lanp", Kind: deployment.KindPortainer, Origin: deployment.OriginAttached, NeedsMode: "emulators", SecretsDelivery: "inject", Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.Deployments.EnsureStanding(ctx, pid, "lan", target.ID, deployment.Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Deployments.OwnerOf(dep).Created(ctx, deploy.Owned{Kind: "volume", ID: "apptown-pl_apptown-data-pl-reports", Name: "apptown-pl_apptown-data-pl-reports", Data: true}); err != nil {
		t.Fatal(err)
	}
	layout, err := portainer.ReadLayout("apptown-pl", "pl", "services:\n  app:\n    volumes: [\"apptown-data-pl:/data\"]\nvolumes:\n  apptown-data-pl: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	d := dockerfake.New()
	d.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) == 3 && args[0] == "volume" && args[1] == "inspect" {
			return []byte("[{}]"), nil, true // both volumes are there
		}
		return nil, nil, false
	})
	held, err := s.heldOnPortainer(ctx, docker.Scope{Client: d, NoStreams: true}, layout, dep)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range held.items {
		if it.Kind != "volume" || it.Copy != CopyLiveFiles {
			t.Fatalf("a data folder is copied as files: %+v", it)
		}
		got[it.Volume] = it.Listed
	}
	if listed, ok := got["apptown-pl_apptown-data-pl-reports"]; !ok || !listed {
		t.Fatalf("the volume an earlier release made is not held as the deployment's: %v", got)
	}
	if listed, ok := got["apptown-pl_apptown-data-pl"]; !ok || listed {
		t.Fatalf("the volume the stack declares and the deploy did not make: %v", got)
	}
}

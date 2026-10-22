package docker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

func TestReapPreviewOrphansSparesDeploymentsAndSteps(t *testing.T) {
	t.Parallel()
	f := dockerfake.New()
	f.SetReply(func(args []string) ([]byte, error, bool) {
		call := strings.Join(args, " ")
		switch {
		case call == "ps -aq --filter label=apptown.class=preview":
			return []byte("prev-1\nprev-2\n"), nil, true
		case strings.HasPrefix(call, "ps -a --filter label=apptown.class=service --format"):
			// a run preview's postgres, a change preview's, a release look's —
			// and an environment's postgres and an agent step's sidecar, which
			// must both stand.
			return []byte("s1\tpv00000000-000c-7000-8000-000000000000\ns2\tpvsnap-change-7\ns3\tpvrel-letterbox-4\ns4\tenv-letterbox-main\ns5\t00000000-007b-7000-8000-000000000000\n"), nil, true
		case strings.HasPrefix(call, "ps -a --filter label=apptown.class=step --format"):
			return []byte("i1\tpv00000000-000c-7000-8000-000000000000\ni2\tapptown-step-99\n"), nil, true
		case call == "network ls --format {{.Name}}":
			return []byte("bridge\napptown-net-pv00000000-000c-7000-8000-000000000000\napptown-net-env-letterbox-main\napptown-net-00000000-007b-7000-8000-000000000000\napptown-net-pvsnap-change-7\n"), nil, true
		case call == "volume ls -q":
			return []byte("forge-cache\napptown-data-apptown-preview-run-3\napptown-data-apptown-env-letterbox-main\napptown-data-app\n"), nil, true
		}
		return nil, nil, false
	})
	docker.ReapPreviewOrphans(context.Background(), docker.Scope{Client: f})
	removed := map[string]bool{}
	for _, args := range f.Commands() {
		c := strings.Join(args, " ")
		if strings.Contains(c, "name=") || strings.Contains(c, "sh -c") {
			t.Fatalf("reaping by name substring or shell pipeline: %s", c)
		}
		if strings.HasPrefix(c, "rm -f ") && !strings.HasPrefix(c, "rm -f -v ") {
			t.Fatalf("a preview's container removed without its anonymous volumes: %s", c)
		}
		for _, p := range []string{"rm -f -v ", "network rm ", "volume rm "} {
			if strings.HasPrefix(c, p) {
				removed[strings.TrimPrefix(c, p)] = true
			}
		}
	}
	for _, want := range []string{"prev-1", "prev-2", "s1", "s2", "s3", "i1",
		"apptown-net-pv00000000-000c-7000-8000-000000000000", "apptown-net-pvsnap-change-7", "apptown-data-apptown-preview-run-3"} {
		if !removed[want] {
			t.Errorf("%s belongs to a preview and must be reaped; removed=%v", want, removed)
		}
	}
	for _, keep := range []string{"s4", "s5", "i2", "bridge", "apptown-net-env-letterbox-main", "apptown-net-00000000-007b-7000-8000-000000000000",
		"forge-cache", "apptown-data-apptown-env-letterbox-main", "apptown-data-app"} {
		if removed[keep] {
			t.Errorf("%s is persistent or someone else's and must stand; removed=%v", keep, removed)
		}
	}
}

// The start-up sweep finds what the daemon holds by label: a preview
// stack's service goes with its anonymous volumes, a deployment's stays.
func TestTheStartUpSweepRemovesAPreviewStacksServiceAndLeavesADeployments(t *testing.T) {
	t.Parallel()
	f := dockerfake.New()
	scope := docker.Scope{Client: f}
	for name, owner := range map[string]string{"svc-preview": "pvsnap-change-7", "svc-main": "env-letterbox-main"} {
		if _, err := f.Run(context.Background(), "", docker.Command{Args: []string{"run", "-d", "--name", name,
			"--label", "apptown.class=service", "--label", "apptown.owner=" + owner, "postgres:16"}}); err != nil {
			t.Fatal(err)
		}
	}
	docker.ReapPreviewOrphans(context.Background(), scope)
	if f.Running("svc-preview") || len(f.Matching("rm -f -v svc-preview")) != 1 {
		t.Fatalf("a preview stack's service was not removed with its volumes: %v", f.Commands())
	}
	if !f.Running("svc-main") {
		t.Fatal("a deployment's service was removed")
	}
}

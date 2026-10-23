package docker_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A throwaway container goes with its anonymous volumes: the shell checks'
// image declares /git, a database image its data folder, a private daemon
// /var/lib/docker, an app image whatever its Dockerfile declares. Removed without -v, each run left one behind, thousands
// a day. -v never touches a named volume.
//
// Kills: rm -f without -v in RemoveLabelled, StopStep or RemoveStack.
func TestThrowawayContainersAreRemovedWithTheirAnonymousVolumes(t *testing.T) {
	t.Parallel()
	step := idstest.N(7)
	for name, remove := range map[string]func(docker.Scope){
		"a folder's writers": func(dk docker.Scope) { _, _ = dk.RemoveLabelled(context.Background(), "apptown.writes=/w") },
		"a step's agents":    func(dk docker.Scope) { dk.StopStep(context.Background(), step) },
		"a step's services":  func(dk docker.Scope) { _ = dk.RemoveStack(context.Background(), "stack-1") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			daemon := dockerfake.New()
			daemon.SetReply(func(args []string) ([]byte, error, bool) {
				if args[0] == "ps" {
					return []byte(fmt.Sprintf("apptown-step-%d-agent\n", step)), nil, true
				}
				return nil, nil, false
			})
			remove(docker.Scope{Client: daemon})
			removals := daemon.Matching("rm ")
			if len(removals) == 0 {
				t.Fatalf("nothing was removed: %v", daemon.Commands())
			}
			for _, rm := range removals {
				if got := strings.Join(rm, " "); !strings.HasPrefix(got, "rm -f -v ") {
					t.Fatalf("removed without its anonymous volumes: %s", got)
				}
			}
		})
	}
}

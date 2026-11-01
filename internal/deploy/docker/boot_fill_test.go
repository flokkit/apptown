package docker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// A boot's fill runs once the needs answer and before any init step, part
// or the public app: a rebuild after AppTown was restored puts the data
// back before anything of the app can write to, or answer from, the new and
// empty data. A failing fill ends the boot with nothing of the app started.
//
// Kills: a fill run after the init steps (a migration on the empty
// database), after the parts, or not at all on an ordinary boot.
func TestAFillRunsAfterTheNeedsAndBeforeAnythingOfTheApp(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte(preparedContract))
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.WriteFile(ws+"/Dockerfile", []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{StaticServer: testStaticServer, DFImage: testDF, Secrets: liveSecrets{}, Needs: liveNeeds{}, Integrations: liveContributions{"{}"}, Routes: liveRoutes{}, Probe: upProbe{}}
	for _, failing := range []bool{false, true} {
		client := &recordingClient{}
		filledAt := -1
		fill := func(network string) error {
			client.mu.Lock()
			filledAt = len(client.calls)
			client.mu.Unlock()
			if failing {
				return errors.New("the copy did not go back")
			}
			return nil
		}
		_, err := NewHost(deps).Boot(context.Background(), BootRequest{Allowed: AuthorisedWhenRequested, Docker: Scope{Client: client}, Name: "apptown-preview-change-1", StackID: "pvsnap-change-1",
			Workspace: ws, Slug: "app", Deployment: "preview", Probe: time.Second, Scratch: t.TempDir(), Fill: fill}, c)
		calls := client.calls
		if filledAt < 0 {
			t.Fatalf("the fill never ran:\n%s", strings.Join(calls, "\n"))
		}
		needUp := -1
		for i, call := range calls {
			if strings.HasPrefix(call, "inspect -f {{if .State.Health}}") && strings.HasSuffix(call, Current.Service("pvsnap-change-1", "postgres")) {
				needUp = i
			}
			appRuns := strings.HasPrefix(call, "run") && (strings.Contains(call, Current.Init("pvsnap-change-1", "migrate")) ||
				strings.Contains(call, Current.Service("pvsnap-change-1", "worker")) || strings.Contains(call, "apptown-preview-change-1 "))
			if appRuns && i < filledAt {
				t.Fatalf("%q ran before the fill:\n%s", call, strings.Join(calls, "\n"))
			}
			if appRuns && failing {
				t.Fatalf("%q ran after a failed fill:\n%s", call, strings.Join(calls, "\n"))
			}
		}
		if needUp < 0 || needUp >= filledAt {
			t.Fatalf("the fill ran before the need answered (need at %d, fill at %d):\n%s", needUp, filledAt, strings.Join(calls, "\n"))
		}
		if failing {
			if be := AsBootError(err); be == nil || be.Phase != PhaseFill {
				t.Fatalf("a failed fill ended the boot with %v", err)
			}
		} else if err != nil {
			t.Fatalf("boot: %v", err)
		}
	}
}

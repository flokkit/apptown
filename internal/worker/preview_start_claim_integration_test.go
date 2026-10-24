//go:build integration

package worker_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/runtimeimages"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// With real Docker: AppTown starting removes every preview, including a
// boot an earlier request admitted that has no folder or app yet: the claim
// waits for it under its lock, then removes it. The first Docker inspection
// is paused after admission, before
// a new preview has any folder or container for remove-all to enumerate.
func TestIntegrationAStartClaimRemovesABootAdmittedBeforeIt(t *testing.T) {
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	name := "apptown-preview-change-" + suffix
	held := &heldInspect{Client: docker.CLI{Binary: "docker"}, only: name, entered: make(chan struct{}), release: make(chan struct{})}
	resume := func() { held.done.Do(func() { close(held.release) }) }
	defer resume()
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: held}
	hs := workertest.StartServer(t, ca, srv)
	p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	t.Cleanup(func() { resume(); _ = p.Remove(context.Background(), name) })
	// Another preview on the same Docker: AppTown package's preview tests
	// run beside this one in CI. The claim this test makes must leave it.
	other := "apptown-preview-other-" + suffix
	if out, err := dockertest.CombinedOutput("create", "--name", other, "--label", dockerlabel.Class+"=preview", runtimeimages.StaticServer()); err != nil {
		t.Fatalf("creating another preview: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = dockertest.Run("rm", "-f", other) })
	tree := siteTree(t, staticSite, map[string]string{"index.html": "old process"})
	c, err := docker.ReadSelectedContract(tree)
	if err != nil {
		t.Fatal(err)
	}
	bootDone := make(chan error, 1)
	go func() {
		st, err := p.Boot(context.Background(), worker.PreviewSpec{Name: name, StackID: "pvreview" + suffix, Slug: "site", Incarnation: "old+digest", ProbeSeconds: 30}, docker.Prepared{Version: docker.PreparedVersion, Contract: c, BrowserEnv: "{}"}, tree)
		if err == nil && st.Error != "" {
			err = fmt.Errorf("boot: %s", st.Error)
		}
		bootDone <- err
	}()
	select {
	case <-held.entered:
	case err := <-bootDone:
		t.Fatalf("boot finished without inspecting: %v", err)
	}
	clearDone := make(chan error, 1)
	go func() { clearDone <- p.Remove(context.Background(), "") }()
	var clearErr error
	cleared := false
	select {
	case clearErr = <-clearDone:
		cleared = true
	case <-time.After(300 * time.Millisecond):
	}
	resume()
	select {
	case err := <-bootDone:
		if err != nil {
			t.Logf("older boot refused: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("boot did not settle")
	}
	if !cleared {
		select {
		case clearErr = <-clearDone:
		case <-time.After(10 * time.Second):
			t.Fatal("clear did not settle")
		}
	}
	if clearErr != nil {
		t.Fatalf("startup clear: %v", clearErr)
	}
	st, err := p.State(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if st.Running {
		t.Fatal("an older admitted boot survived the confirmed startup clear because it had no folder or app when enumerated")
	}
	if dockertest.Run("inspect", other) != nil {
		t.Fatal("this test's claim removed a preview that is not its own")
	}
}

// heldInspect holds every inspection on the real Docker until release
// closes; entered closes when the first arrives. The claim removes every
// preview its Docker lists, and this Docker is shared with other tests (the
// integration job runs packages at once on one daemon), so the claim's
// listing shows the test's own preview only.
type heldInspect struct {
	docker.Client
	only             string
	entered, release chan struct{}
	first            atomic.Bool
	done             sync.Once
}

func (h *heldInspect) Run(ctx context.Context, host string, c docker.Command) ([]byte, error) {
	if len(c.Args) > 0 && c.Args[0] == "ps" && slices.Contains(c.Args, "label="+dockerlabel.Class+"=preview") {
		c.Args = append(slices.Clone(c.Args), "--filter", "name=^"+h.only)
	}
	if len(c.Args) > 0 && c.Args[0] == "inspect" {
		select {
		case <-h.release:
		default:
			if h.first.CompareAndSwap(false, true) {
				close(h.entered)
			}
			<-h.release
		}
	}
	return h.Client.Run(ctx, host, c)
}

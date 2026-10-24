package worker_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

// A failed recovery claim must leave the lost-fence barrier intact. The
// obstruction deterministically fails rename, without requiring disk exhaustion.
func TestAFailedClaimKeepsTheLostFenceBarrier(t *testing.T) {
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	state := t.TempDir()
	first := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Previews: binary}
	hs := workertest.StartServer(t, ca, first)
	client := workertest.Client(t, ca, workertest.Installation)
	client.Transport = laterBootTransport{base: client.Transport}
	p := worker.Previews{URL: hs.URL, Client: client, Session: "2"}
	if code := fencedDelete(t, p, "", "2", "1"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	name := "apptown-preview-change-971"
	seedPreview(t, state, dk, name)
	if err := reusePreview(t, p, name); err != nil {
		t.Fatal(err)
	}
	if code := fencedDelete(t, p, "/"+name, "2", "2"); code != http.StatusConflict {
		t.Fatal(code)
	}
	hs.Close()
	fence := filepath.Join(state, "previews", "fence.json")
	if err := os.Remove(fence); err != nil {
		t.Fatal(err)
	}
	restarted := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Previews: binary}
	restarted.SweepPreviews(context.Background(), true)
	hs2 := workertest.StartServer(t, ca, restarted)
	p.URL = hs2.URL
	if code := fencedDelete(t, p, "/"+name, "2", "2"); code != http.StatusConflict {
		t.Fatalf("lost fence did not refuse: %d", code)
	}
	if err := os.MkdirAll(filepath.Join(fence, "obstruction"), 0700); err != nil {
		t.Fatal(err)
	}
	// This can be a delayed claim from before the successful sequence-100 reuse.
	if code := fencedDelete(t, p, "", "2", "1"); code != http.StatusInternalServerError {
		t.Fatalf("claim did not fail: %d", code)
	}
	if !exists(filepath.Join(dk, "running-"+name)) {
		t.Fatal("failed claim already removed preview")
	}
	if err := os.RemoveAll(fence); err != nil {
		t.Fatal(err)
	}
	code := fencedDelete(t, p, "/"+name, "2", "2")
	if code != http.StatusConflict || !exists(filepath.Join(dk, "running-"+name)) {
		t.Fatalf("failed recovery claim lifted the barrier: old removal returned %d; preserved preview retained=%v", code, exists(filepath.Join(dk, "running-"+name)))
	}
}

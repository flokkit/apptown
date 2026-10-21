package worker_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// A failed post-rename recovery write must stay fail-closed after restart.
// server.go has a review-only marker-triggered fault after the real rename.
func TestAClaimThatFailedAfterItsRenameKeepsTheBarrierAcrossARestart(t *testing.T) {
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	state := t.TempDir()
	first := &worker.Server{Installation: workertest.Installation, State: state, Previews: binary}
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
	restarted := &worker.Server{Installation: workertest.Installation, State: state, Previews: binary}
	restarted.SweepPreviews(context.Background(), true)
	hs2 := workertest.StartServer(t, ca, restarted)
	p.URL = hs2.URL
	if code := fencedDelete(t, p, "/"+name, "2", "2"); code != http.StatusConflict {
		t.Fatalf("lost fence did not refuse: %d", code)
	}
	// Not parallel: the fault is the process's (export_test.go).
	restore := worker.FailAfterRename("fence.json", errors.New("the folder could not be synced"))
	// This can be a delayed claim from before the successful sequence-100 reuse.
	if code := fencedDelete(t, p, "", "2", "1"); code != http.StatusInternalServerError {
		t.Fatalf("claim did not fail: %d", code)
	}
	if !exists(filepath.Join(dk, "running-"+name)) {
		t.Fatal("failed claim already removed preview")
	}
	restore()
	if code := fencedDelete(t, p, "/"+name, "2", "2"); code != http.StatusConflict {
		t.Fatalf("same server lost barrier: %d", code)
	}
	persisted, err := os.ReadFile(fence)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fence left by failed claim: %s", persisted)
	hs2.Close()
	third := &worker.Server{Installation: workertest.Installation, State: state, Previews: binary}
	third.SweepPreviews(context.Background(), true)
	hs3 := workertest.StartServer(t, ca, third)
	p.URL = hs3.URL
	code := fencedDelete(t, p, "/"+name, "2", "2")
	if code != http.StatusConflict || !exists(filepath.Join(dk, "running-"+name)) {
		t.Fatalf("restart after failed post-rename claim lifted the barrier: old removal returned %d; preserved preview retained=%v", code, exists(filepath.Join(dk, "running-"+name)))
	}
}

// A claim that removes every kept preview ends a lost fence: the worker
// admits requests after it again, and still does after it starts again.
func TestASuccessfulClaimEndsALostFence(t *testing.T) {
	t.Parallel()
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	state := t.TempDir()
	name := "apptown-preview-change-903"
	seedPreview(t, state, dk, name)
	if err := os.Remove(filepath.Join(state, "previews", "fence.json")); err != nil {
		t.Fatal(err)
	}
	srv := &worker.Server{Installation: workertest.Installation, State: state, Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation), Session: "3"}
	if code := fencedDelete(t, p, "/apptown-preview-run-1", "3", "1"); code != http.StatusConflict {
		t.Fatalf("a lost fence admitted a request: %d", code)
	}
	if code := fencedDelete(t, p, "", "3", "2"); code != http.StatusNoContent || exists(filepath.Join(dk, "running-"+name)) {
		t.Fatalf("the recovery claim: %d", code)
	}
	if code := fencedDelete(t, p, "/apptown-preview-run-1", "3", "3"); code != http.StatusNoContent {
		t.Fatalf("a request after the recovery claim: %d", code)
	}
	hs.Close()
	restarted := &worker.Server{Installation: workertest.Installation, State: state, Previews: binary}
	hs2 := workertest.StartServer(t, ca, restarted)
	p.URL = hs2.URL
	if code := fencedDelete(t, p, "/apptown-preview-run-1", "3", "4"); code != http.StatusNoContent {
		t.Fatalf("a request after the worker started again: %d", code)
	}
}

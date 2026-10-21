package worker_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// Losing the fence must not turn a preserved preview into an unfenced one.
// This models the on-disk result, not a power-failure filesystem simulation.
func TestALostFenceAdmitsNothingButAClaim(t *testing.T) {
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	state := t.TempDir()
	srv := &worker.Server{Installation: workertest.Installation, State: state, Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	client := workertest.Client(t, ca, workertest.Installation)
	client.Transport = laterBootTransport{base: client.Transport}
	p := worker.Previews{URL: hs.URL, Client: client, Session: "2"}
	if code := fencedDelete(t, p, "", "2", "1"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	name := "apptown-preview-change-901"
	seedPreview(t, state, dk, name)
	if err := reusePreview(t, p, name); err != nil {
		t.Fatal(err)
	}
	if code := fencedDelete(t, p, "/"+name, "2", "2"); code != http.StatusConflict {
		t.Fatal(code)
	}
	hs.Close()
	if err := os.Remove(filepath.Join(state, "previews", "fence.json")); err != nil {
		t.Fatal(err)
	}
	restarted := &worker.Server{Installation: workertest.Installation, State: state, Previews: binary}
	restarted.SweepPreviews(context.Background(), true)
	hs2 := workertest.StartServer(t, ca, restarted)
	p.URL = hs2.URL
	code := fencedDelete(t, p, "/"+name, "2", "2")
	if !exists(filepath.Join(dk, "running-"+name)) {
		t.Fatalf("lost fence: old removal returned %d and deleted the preserved, later boot", code)
	}
}

// Once a confirmed claim supersedes removed names, the next startup should
// not run Docker removals for every preview that has ever existed.
func TestAClaimDoesNotRevisitRemovedHistory(t *testing.T) {
	binary, _ := sweepDocker(t)
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation), Session: "2"}
	// Two removed names: a claim that forgets none, or only one, of them
	// revisits the other at the next claim.
	for i := 1; i <= 2; i++ {
		name := fmt.Sprintf("/apptown-preview-run-%d", i)
		if code := fencedDelete(t, p, name, "1", fmt.Sprint(i)); code != http.StatusNoContent {
			t.Fatal(code)
		}
	}
	if code := fencedDelete(t, p, "", "2", "1"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	removals := len(binary.commands("rm"))
	if code := fencedDelete(t, p, "", "3", "1"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if n := len(binary.commands("rm")) - removals; n != 0 {
		t.Fatalf("empty worker's next claim repeated %d container removals for already removed history", n)
	}
	// Compaction must keep refusing requests older than the claim.
	if code := fencedDelete(t, p, "/apptown-preview-run-1", "1", "100"); code != http.StatusConflict {
		t.Fatal(code)
	}
}

func TestACorruptSessionCountIsAnError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "worker-session")
	if err := os.WriteFile(file, []byte("truncated-number\n"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := worker.NextSession(file, nil)
	if err == nil {
		t.Fatalf("corrupt session counter silently replaced by clock session %d", next)
	}
}

// Admission writes must fail before effects. A non-empty directory where the
// fence is renamed into place deterministically fails its durable write,
// without depending on user privileges.
func TestAFenceThatCannotBeWrittenAdmitsNothing(t *testing.T) {
	for _, action := range []string{"boot", "remove", "claim"} {
		t.Run(action, func(t *testing.T) {
			binary, dk := sweepDocker(t)
			ca := workertest.NewCA(t)
			srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
			hs := workertest.StartServer(t, ca, srv)
			p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation), Session: "2"}
			if code := fencedDelete(t, p, "", "2", "1"); code != http.StatusNoContent {
				t.Fatal(code)
			}
			name := "apptown-preview-change-902"
			seedPreview(t, srv.State, dk, name)
			tmp := filepath.Join(srv.State, "previews", "fence.json")
			if err := os.RemoveAll(tmp); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(tmp, "blocked"), 0700); err != nil {
				t.Fatal(err)
			}
			if action == "boot" {
				if err := reusePreview(t, p, name); err == nil {
					t.Fatal("boot admitted without its fence write")
				}
			} else {
				suffix := "/" + name
				if action == "claim" {
					suffix = ""
				}
				if code := fencedDelete(t, p, suffix, "2", "100"); code != http.StatusInternalServerError {
					t.Fatal(code)
				}
			}
			if !exists(filepath.Join(dk, "running-"+name)) {
				t.Fatal("failed admission removed preview")
			}
			if err := os.RemoveAll(tmp); err != nil {
				t.Fatal(err)
			}
			if code := fencedDelete(t, p, "/"+name, "3", "1"); code != http.StatusNoContent {
				t.Fatal(code)
			}
		})
	}
}

func TestACorruptFenceAdmitsNothing(t *testing.T) {
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	name := "apptown-preview-change-903"
	seedPreview(t, srv.State, dk, name)
	if err := os.WriteFile(filepath.Join(srv.State, "previews", "fence.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	hs := workertest.StartServer(t, ca, srv)
	p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	if code := fencedDelete(t, p, "/"+name, "2", "1"); code != http.StatusInternalServerError {
		t.Fatal(code)
	}
	if !exists(filepath.Join(dk, "running-"+name)) {
		t.Fatal("corrupt fence permitted removal")
	}
}

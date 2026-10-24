package worker_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

// A recovery claim already persisted on the worker must not be lowered by
// an older claim that arrives while its cleanup is still in progress.
func TestOverlappingRecoveryClaimsKeepTheNewestFloor(t *testing.T) {
	for _, olderSession := range []string{"2", "3"} {
		t.Run("older-session-"+olderSession, func(t *testing.T) {
			binary, dk := sweepDocker(t)
			entered, resume := binary.holdFirst("ps")
			state := t.TempDir()
			name := "apptown-preview-change-981"
			seedPreview(t, state, dk, name)
			fence := filepath.Join(state, "previews", "fence.json")
			if err := os.Remove(fence); err != nil {
				t.Fatal(err)
			}
			ca := workertest.NewCA(t)
			srv := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Previews: binary}
			hs := workertest.StartServer(t, ca, srv)
			// Registered after server cleanup so a failed assertion cannot strand it.
			t.Cleanup(resume)
			p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation), Session: "3"}
			if code := fencedDelete(t, p, "/"+name, "3", "1"); code != http.StatusConflict {
				t.Fatalf("fixture: lost fence returned %d", code)
			}
			req, err := http.NewRequest(http.MethodDelete, p.URL+"/v1/previews", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(worker.PreviewSessionHeader, "3")
			req.Header.Set(worker.PreviewSeqHeader, "10")
			type result struct {
				code int
				err  error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := p.Client.Do(req)
				if err != nil {
					done <- result{err: err}
					return
				}
				resp.Body.Close()
				done <- result{code: resp.StatusCode}
			}()
			// The claim reaching its Docker listing is the synchronisation point.
			select {
			case <-entered:
			case got := <-done:
				t.Fatalf("new claim answered without reaching Docker listing: %+v", got)
			}
			// The newer claim's floor is on disk, but Lost remains set during cleanup.
			before, err := os.ReadFile(fence)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("new claim waiting for Docker: %s", before)
			olderCode := fencedDelete(t, p, "", olderSession, "1")
			if olderCode != http.StatusNoContent && olderCode != http.StatusConflict {
				t.Fatalf("older claim: %d", olderCode)
			}
			resume()
			if got := <-done; got.err != nil || got.code != http.StatusNoContent {
				t.Fatalf("newer claim: %+v", got)
			}
			if exists(filepath.Join(dk, "running-"+name)) {
				t.Fatal("recovery did not remove the kept preview")
			}
			after, err := os.ReadFile(fence)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("after newer claim confirmed (older claim returned %d): %s", olderCode, after)
			// This ordinary request is older than the successfully confirmed (3,10)
			// claim. The same admission check also gates delayed boots of absent names.
			if code := fencedDelete(t, p, "/apptown-preview-run-982", olderSession, "2"); code != http.StatusConflict {
				t.Errorf("older request admitted after newer recovery claim confirmed: %d", code)
			}
			hs.Close()
			restarted := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Previews: binary}
			hs2 := workertest.StartServer(t, ca, restarted)
			p.URL = hs2.URL
			if code := fencedDelete(t, p, "/apptown-preview-run-983", olderSession, "3"); code != http.StatusConflict {
				t.Errorf("older request admitted after restart too: %d", code)
			}
		})
	}
}

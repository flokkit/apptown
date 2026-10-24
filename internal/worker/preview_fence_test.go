package worker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

func fencedDelete(t *testing.T, p worker.Previews, name, session, seq string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, p.URL+"/v1/previews"+name, nil)
	if err != nil {
		t.Fatal(err)
	}
	if session != "" {
		req.Header.Set(worker.PreviewSessionHeader, session)
	}
	if seq != "" {
		req.Header.Set(worker.PreviewSeqHeader, seq)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func seedPreview(t *testing.T, state, dk, name string) {
	t.Helper()
	folder := previewFolder(t, state, name)
	for path, data := range map[string]string{filepath.Join(folder, "booted"): "inc", filepath.Join(dk, "running-"+name): ""} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func reusePreview(t *testing.T, p worker.Previews, name string) error {
	t.Helper()
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := p.Boot(context.Background(), worker.PreviewSpec{Name: name, StackID: "pv1", Slug: "site", Incarnation: "inc", ProbeSeconds: 1}, docker.Prepared{Version: docker.PreparedVersion, Contract: c}, t.TempDir())
	if err == nil && !st.Running {
		t.Fatalf("fixture: reuse not running: %+v", st)
	}
	return err
}

// A failed first startup clear can arrive after its successful retry and
// the first subsequent boot. Claims must obey ordering too, across both
// retries in one process and delayed requests from the preceding process.
func TestALateClaimNeverUndoesAConfirmedClaimAndBoot(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(strconv.FormatBool(previous), func(t *testing.T) {
			binary, dk := sweepDocker(t)
			ca := workertest.NewCA(t)
			srv := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
			hs := workertest.StartServer(t, ca, srv)
			p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation), Session: "2"}
			p.Client.Transport = laterBootTransport{base: p.Client.Transport}
			if code := fencedDelete(t, p, "", "2", "2"); code != http.StatusNoContent {
				t.Fatal(code)
			}
			name := "apptown-preview-change-61"
			seedPreview(t, srv.State, dk, name)
			if err := reusePreview(t, p, name); err != nil {
				t.Fatal(err)
			}
			session := "2"
			if previous {
				session = "1" // the preceding apptown start
			}
			code := fencedDelete(t, p, "", session, "1")
			st, err := p.State(context.Background(), name)
			if err != nil {
				t.Fatal(err)
			}
			if !st.Running {
				t.Fatalf("late startup claim returned %d and removed the successfully reused preview", code)
			}
			if err := reusePreview(t, p, name); err != nil {
				t.Fatalf("late claim fenced the current AppTown out: %v", err)
			}
			// The late claim did not lower the fence: a request older than
			// the confirmed claim is still refused, even for another preview.
			if code := fencedDelete(t, p, "/apptown-preview-run-7", "1", "5"); code != http.StatusConflict {
				t.Fatalf("a request older than the confirmed claim, after the late claim: %d", code)
			}
		})
	}
}

// Invalid fencing metadata must be rejected before changing ownership or
// deleting any preview. In particular, the empty session is also the
// implementation's sentinel for a worker with no owner.
func TestAClaimRequiresItsSessionAndNumber(t *testing.T) {
	for _, headers := range [][2]string{{"", ""}, {"2", ""}, {"2", "bad"}, {"2", "0"}, {"2", "-1"}} {
		t.Run(headers[0]+"_"+headers[1], func(t *testing.T) {
			binary, dk := sweepDocker(t)
			ca := workertest.NewCA(t)
			srv := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
			hs := workertest.StartServer(t, ca, srv)
			p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation), Session: "2"}
			// On a worker never claimed, whose fence is still zero.
			name := "apptown-preview-change-62"
			seedPreview(t, srv.State, dk, name)
			code := fencedDelete(t, p, "", headers[0], headers[1])
			if code < 400 || !exists(filepath.Join(dk, "running-"+name)) {
				t.Fatalf("invalid claim on a fresh worker returned %d; preview retained=%v", code, exists(filepath.Join(dk, "running-"+name)))
			}
			// And after a claim.
			if code := fencedDelete(t, p, "", "2", "1"); code != http.StatusNoContent {
				t.Fatal(code)
			}
			seedPreview(t, srv.State, dk, name)
			code = fencedDelete(t, p, "", headers[0], headers[1])
			if code < 400 || !exists(filepath.Join(dk, "running-"+name)) {
				t.Fatalf("invalid claim returned %d; preview retained=%v", code, exists(filepath.Join(dk, "running-"+name)))
			}
		})
	}
}

// A delayed request not yet received by the worker can cross a worker
// restart. Reconstructing Server preserves its disk, just as a process
// restart does, but drops its in-memory session. An old reconciliation
// DELETE for an absent name must not strand the current AppTown session.
func TestAWorkerRestartKeepsTheLatestClaim(t *testing.T) {
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	state := t.TempDir()
	first := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Previews: binary}
	hs := workertest.StartServer(t, ca, first)
	client := workertest.Client(t, ca, workertest.Installation)
	p := worker.Previews{URL: hs.URL, Client: client, Session: "2"}
	if code := fencedDelete(t, p, "", "1", "1"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if code := fencedDelete(t, p, "", "2", "1"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	name := "apptown-preview-change-63"
	seedPreview(t, state, dk, name)
	if err := reusePreview(t, p, name); err != nil {
		t.Fatal(err)
	}
	hs.Close()
	restarted := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Previews: binary}
	restarted.SweepPreviews(context.Background(), true)
	hs2 := workertest.StartServer(t, ca, restarted)
	p.URL = hs2.URL
	code := fencedDelete(t, p, "/apptown-preview-run-999", "1", "2")
	if code != http.StatusConflict {
		t.Fatalf("a request of the earlier session after the worker restarted: %d", code)
	}
	if err := reusePreview(t, p, name); err != nil {
		t.Fatalf("old request returned %d and chose the restarted worker's owner; current AppTown cannot reuse its preserved preview: %v", code, err)
	}
}

// A live AppTown can have an older removal waiting to dispatch while a later
// boot succeeds. Keeping the container across a worker restart must keep
// the ordering fence too; AppTown's pending request outlives that worker.
func TestAWorkerRestartKeepsRequestOrder(t *testing.T) {
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	state := t.TempDir()
	first := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Previews: binary}
	hs := workertest.StartServer(t, ca, first)
	client := workertest.Client(t, ca, workertest.Installation)
	// Use a fixed later sequence for the real reuse, so this test is
	// independent of the process-wide counter used by other tests.
	client.Transport = laterBootTransport{base: client.Transport}
	p := worker.Previews{URL: hs.URL, Client: client, Session: "2"}
	if code := fencedDelete(t, p, "", "2", "1"); code != http.StatusNoContent {
		t.Fatal(code)
	}
	name := "apptown-preview-change-64"
	seedPreview(t, state, dk, name)
	if err := reusePreview(t, p, name); err != nil {
		t.Fatal(err)
	}
	// Positive control: the older request is refused before the restart.
	if code := fencedDelete(t, p, "/"+name, "2", "2"); code != http.StatusConflict {
		t.Fatalf("fixture: old removal before restart: %d", code)
	}
	hs.Close()
	restarted := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: state, Previews: binary}
	restarted.SweepPreviews(context.Background(), true)
	hs2 := workertest.StartServer(t, ca, restarted)
	p.URL = hs2.URL
	code := fencedDelete(t, p, "/"+name, "2", "2")
	st, err := p.State(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running {
		t.Fatalf("old removal refused before restart returned %d afterwards and removed the later successful boot", code)
	}
}

type laterBootTransport struct{ base http.RoundTripper }

func (r laterBootTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPut {
		req = req.Clone(req.Context())
		req.Header.Set(worker.PreviewSeqHeader, "100")
	}
	return r.base.RoundTrip(req)
}

// This is the actual overlap, not just out-of-order arrival: the first
// claim has changed the session and is stuck listing Docker. Its reply is
// lost; the retry completes and a boot reuses successfully before the first
// claim resumes. The old claim must not remove that later boot.
func TestAClaimInFlightNeverUndoesItsRetryAndBoot(t *testing.T) {
	binary, dk := sweepDocker(t)
	entered, resume := binary.holdFirst("ps")
	defer resume()
	ca := workertest.NewCA(t)
	srv := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	var first atomic.Bool
	settled := make(chan struct{})
	hs := workertest.StartWith(t, ca, srv.State, nil, func(_ http.Handler) http.Handler {
		next := srv.Handler()
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete && r.URL.Path == "/v1/previews" && first.CompareAndSwap(false, true) {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					panic(err)
				}
				conn.Close()
				next.ServeHTTP(httptest.NewRecorder(), r)
				close(settled)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	p := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation), Session: "2"}
	name := "apptown-preview-change-65"
	seedPreview(t, srv.State, dk, name)
	if err := p.Remove(context.Background(), ""); err == nil {
		t.Fatal("fixture: lost claim reply succeeded")
	}
	// The claim reaching its Docker listing is the synchronisation point.
	select {
	case <-entered:
	case <-settled:
		t.Fatal("the claim finished without listing Docker")
	}
	if err := p.Remove(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	seedPreview(t, srv.State, dk, name)
	if err := reusePreview(t, p, name); err != nil {
		t.Fatal(err)
	}
	resume()
	<-settled // an old claim that never finishes meets the test binary's deadline
	st, err := p.State(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running {
		t.Fatal("claim already in flight removed the successful boot after its retry confirmed startup clear")
	}
}

// An AppTown's session numbers keep growing across its starts, whatever the
// clock says: each is greater than the last one kept in its file.
func TestAnAppTownsSessionsKeepGrowing(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "worker-session")
	ahead := time.Now().Add(time.Hour).UnixNano() // a start while the clock ran ahead
	if err := os.WriteFile(file, []byte(strconv.FormatInt(ahead, 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := worker.NextSession(file, nil)
	if err != nil || first <= ahead {
		t.Fatalf("after a start ahead of the clock: %d %v", first, err)
	}
	second, err := worker.NextSession(file, nil)
	if err != nil || second <= first {
		t.Fatalf("the next start: %d after %d, %v", second, first, err)
	}
}

package worker_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
	"github.com/flokkit/apptown/stock"
)

// A worker serves the AppTown installations whose protocol is in its range and refuses
// any other by name, and a request that states no protocol. Its version
// answers any caller of its installation.
//
// Kills: a worker that serves any AppTown, whatever protocol it speaks, or
// one that states none.
func TestAWorkerServesOnlyAppTownProtocolsInItsRange(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	hs := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir()})
	client := workertest.BareClient(t, ca, workertest.Installation)
	get := func(protocol string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+"/v1/disks", nil)
		if protocol != "" {
			req.Header.Set(worker.ProtocolHeader, protocol)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	for _, p := range []string{"1", "2"} {
		if resp := get(p); resp.StatusCode != 200 {
			t.Errorf("the worker refused protocol %q: %s", p, resp.Status)
		}
	}
	for _, p := range []string{"3", "0", "x", ""} {
		resp := get(p)
		raw, _ := io.ReadAll(resp.Body)
		body := string(raw)
		if resp.StatusCode != http.StatusUpgradeRequired || !strings.Contains(body, "serves AppTown protocols 1 to 2") || !strings.Contains(body, "same release") {
			t.Errorf("the worker served protocol %q: %s %q", p, resp.Status, body)
		}
	}
	vreq, _ := http.NewRequest("GET", hs.URL+"/v1/version", nil)
	vresp, err := client.Do(vreq)
	if err != nil {
		t.Fatal(err)
	}
	var v worker.VersionReport
	if err := json.NewDecoder(vresp.Body).Decode(&v); err != nil || v.Version == "" || v.ProtocolMin != worker.ProtocolMin || v.ProtocolMax != worker.ProtocolMax {
		t.Errorf("the worker's version report: %+v %v", v, err)
	}
	vresp.Body.Close()
}

// An AppTown's transport to its worker states the protocol it speaks on every
// request.
func TestAnAppTownStatesItsProtocolToItsWorker(t *testing.T) {
	t.Parallel()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get(worker.ProtocolHeader))
	}))
	defer srv.Close()
	client := &http.Client{Transport: worker.SpeakProtocol(nil)}
	for _, m := range []string{"GET", "POST"} {
		req, _ := http.NewRequest(m, srv.URL+"/v1/tasks/k", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if len(got) != 2 || got[0] != "2" || got[1] != "2" {
		t.Fatalf("protocol sent: %q", got)
	}
}

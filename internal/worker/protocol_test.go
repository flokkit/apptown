package worker_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// A worker serves the AppTown installations whose protocol is in its range and refuses
// any other by name, an agent worker and a check worker alike. A request
// that states no protocol comes from an AppTown built before protocols were
// stated, and is served.
//
// Kills: a worker that serves any AppTown, whatever protocol it speaks.
func TestAWorkerServesOnlyTheForgeProtocolsInItsRange(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	for _, role := range []string{worker.RoleAgent, worker.RoleCheck} {
		hs := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Role: worker.Role{Role: role}})
		client := workertest.Client(t, ca, workertest.Installation)
		get := func(protocol string) *http.Response {
			t.Helper()
			req, _ := http.NewRequest("GET", hs.URL+"/v1/role", nil)
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
		for _, p := range []string{"", "1", "2"} {
			if resp := get(p); resp.StatusCode != 200 {
				t.Errorf("%s worker refused protocol %q: %s", role, p, resp.Status)
			}
		}
		for _, p := range []string{"3", "0", "x"} {
			resp := get(p)
			raw, _ := io.ReadAll(resp.Body)
			body := string(raw)
			if resp.StatusCode != http.StatusUpgradeRequired || !strings.Contains(body, "serves AppTown protocols 1 to 2") || !strings.Contains(body, "same release") {
				t.Errorf("%s worker served protocol %q: %s %q", role, p, resp.Status, body)
			}
		}
		vreq, _ := http.NewRequest("GET", hs.URL+"/v1/version", nil)
		vresp, err := client.Do(vreq)
		if err != nil {
			t.Fatal(err)
		}
		var v worker.VersionReport
		if err := json.NewDecoder(vresp.Body).Decode(&v); err != nil || v.Version == "" || v.ProtocolMin != worker.ProtocolMin || v.ProtocolMax != worker.ProtocolMax {
			t.Errorf("%s worker's version report: %+v %v", role, v, err)
		}
		vresp.Body.Close()
	}
}

// An AppTown's transport to its worker states the protocol it speaks on every
// request.
func TestAForgeStatesItsProtocolToItsWorker(t *testing.T) {
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

// An AppTown that a check worker does not serve stops at startup with the
// worker's own sentence: both protocols and what to install, not only the
// HTTP status.
func TestAForgeACheckWorkerDoesNotServeHearsWhyAtStartup(t *testing.T) {
	t.Parallel()
	ca := workertest.NewCA(t)
	hs := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Role: worker.Role{Role: worker.RoleCheck}})
	client := workertest.Client(t, ca, workertest.Installation)
	next := client.Transport
	client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set(worker.ProtocolHeader, "3") // an AppTown from a later release
		return next.RoundTrip(r)
	})
	err := worker.RequireCheckWorker(t.Context(), client, hs.URL, time.Now())
	if err == nil || !strings.Contains(err.Error(), "426") || !strings.Contains(err.Error(), "serves AppTown protocols 1 to 2") || !strings.Contains(err.Error(), "same release") {
		t.Fatalf("startup refusal: %v", err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

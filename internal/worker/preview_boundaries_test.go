package worker_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"

	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

func TestAVisitorsForwardingHeadersNeverReachTheApp(t *testing.T) {
	forge, _, _, got, _ := forwardedPreview(t)
	req, _ := http.NewRequest("GET", forge.URL+"/", nil)
	req.Header.Set("X-Forwarded-Prefix", "/attacker")
	req.Header.Set("X-Forwarded-Port", "12345")
	req.Header["x-forwarded-ssl"] = []string{"on"} // sent as written
	req.Header.Set("Forwarded", "for=6.6.6.6;proto=http")
	req.Header.Set("X-Real-Ip", "6.6.6.6")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, h := range []string{"X-Forwarded-Prefix", "X-Forwarded-Port", "X-Forwarded-Ssl", "Forwarded", "X-Real-Ip"} {
		if v := got().Header.Get(h); v != "" {
			t.Errorf("visitor's %s reached the app: %q", h, v)
		}
	}
}

// spec.json is written before Boot. A worker killed during its readiness
// probe leaves exactly this running app plus spec, with no completion record.
func TestAnInterruptedBootIsRemovedWhenTheWorkerStarts(t *testing.T) {
	binary, dk := sweepDocker(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	dir := previewFolder(t, srv.State, "apptown-preview-change-42")
	if err := os.WriteFile(filepath.Join(dk, "running-apptown-preview-change-42"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	srv.SweepPreviews(context.Background(), true)
	if exists(dir) || exists(filepath.Join(dk, "running-apptown-preview-change-42")) {
		t.Fatal("startup kept the app/tree of a boot interrupted before readiness completed")
	}
}

func TestAPreviewMarkedForRemovalIsNeverReused(t *testing.T) {
	binary, dk := sweepDocker(t)
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	name := "apptown-preview-change-43"
	dir := previewFolder(t, srv.State, name)
	for _, f := range []string{filepath.Join(dir, "removing"), filepath.Join(dk, "running-"+name)} {
		if err := os.WriteFile(f, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := previews.Boot(context.Background(), worker.PreviewSpec{Name: name, StackID: "pv1", Slug: "app", Incarnation: "inc", ProbeSeconds: 1}, docker.Prepared{Version: docker.PreparedVersion, Contract: c}, t.TempDir())
	if err == nil && st.Running && st.ContainerID == "id-"+name {
		t.Fatalf("reused a known failed boot that is marked for removal: %+v", st)
	}
}

func TestRemovingEveryPreviewFailsWhenDockerCannotList(t *testing.T) {
	binary := unavailableDocker()
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: binary}
	hs := workertest.StartServer(t, ca, srv)
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	if err := previews.Remove(context.Background(), ""); err == nil {
		t.Fatal("discard-all answered success without being able to list Docker containers")
	}
}

func TestAnEmptyQuerySurvivesBothHops(t *testing.T) {
	forge, _, _, got, _ := forwardedPreview(t)
	req, _ := http.NewRequest("GET", forge.URL+"/path?", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got().RequestURI != "/path?" {
		t.Fatalf("request target changed: got %q, want /path?", got().RequestURI)
	}
}

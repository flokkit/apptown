//go:build integration

package worker_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

type previewWorker struct {
	previews worker.Previews
	srv      *worker.Server
	name     string
	stack    string
}

func startPreviewWorker(t *testing.T) previewWorker {
	t.Helper()
	ca := workertest.NewCA(t)
	srv := &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: docker.CLI{Binary: "docker"}}
	hs := workertest.StartServer(t, ca, srv)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	w := previewWorker{previews: worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)},
		srv: srv, name: "forge-preview-change-" + suffix, stack: "pvtest" + suffix}
	t.Cleanup(func() { _ = w.previews.Remove(context.Background(), w.name) })
	return w
}

func siteTree(t *testing.T, contract string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files[".apptown/app.yaml"] = contract
	for path, body := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const staticSite = "version: 1\napp:\n  web:\n    static: .\n    public: true\n"

// boot boots tree on the worker, prepared for the contract the tree
// declares.
func (w previewWorker) boot(t *testing.T, incarnation, tree string) worker.PreviewState {
	t.Helper()
	c, err := docker.ReadSelectedContract(tree)
	if err != nil {
		t.Fatal(err)
	}
	st, err := w.previews.Boot(context.Background(), worker.PreviewSpec{Name: w.name, StackID: w.stack, Slug: "site",
		Incarnation: incarnation, ProbeSeconds: 60}, docker.Prepared{Version: docker.PreparedVersion, Contract: c, BrowserEnv: "{}"}, tree)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// served is the page AppTown's forwarding returns for the preview now.
func (w previewWorker) served(t *testing.T) string {
	t.Helper()
	forge := httptest.NewServer(w.previews.Forward(w.name, "https"))
	defer forge.Close()
	resp, err := http.Get(forge.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(body))
}

func containersNamed(t *testing.T, filter string) string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-aq", "--filter", filter).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// With real Docker: a static site booted on the worker is served, through
// both hops, after the boot has answered; the same incarnation asked again
// is the preview already running, whatever tree came with it; another
// incarnation replaces it with its own tree; and removal leaves nothing.
func TestIntegrationAWorkerPreviewIsServedKeptAndReplacedByIdentity(t *testing.T) {
	w := startPreviewWorker(t)
	first := w.boot(t, "commit-a+digest", siteTree(t, staticSite, map[string]string{"index.html": "first"}))
	if !first.Running || first.Incarnation != "commit-a+digest" || first.Error != "" {
		t.Fatalf("the boot answered %+v", first)
	}
	if got := w.served(t); got != "first" {
		t.Fatalf("served %q", got)
	}

	again := w.boot(t, "commit-a+digest", siteTree(t, staticSite, map[string]string{"index.html": "not booted"}))
	if again.ContainerID != first.ContainerID {
		t.Fatalf("the same incarnation booted again: %s, then %s", first.ContainerID, again.ContainerID)
	}
	if got := w.served(t); got != "first" {
		t.Fatalf("a kept preview's tree changed: served %q", got)
	}

	next := w.boot(t, "commit-b+digest", siteTree(t, staticSite, map[string]string{"index.html": "second"}))
	if !next.Running || next.ContainerID == first.ContainerID || next.Incarnation != "commit-b+digest" {
		t.Fatalf("the replacement answered %+v", next)
	}
	if got := w.served(t); got != "second" {
		t.Fatalf("served %q", got)
	}
	if containersNamed(t, "id="+first.ContainerID) != "" {
		t.Fatal("the replaced incarnation is still there")
	}

	if err := w.previews.Remove(context.Background(), w.name); err != nil {
		t.Fatal(err)
	}
	st, err := w.previews.State(context.Background(), w.name)
	if err != nil || st.Running || st.ContainerID != "" {
		t.Fatalf("after removal: %+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(w.srv.State, "previews", w.name)); !os.IsNotExist(err) {
		t.Fatalf("the preview's tree is still on the worker: %v", err)
	}
}

// With real Docker: a boot that fails part way answers its phase and error,
// as a local boot does, and leaves nothing of its stack on the worker.
func TestIntegrationAFailedWorkerBootLeavesNothing(t *testing.T) {
	w := startPreviewWorker(t)
	contract := "version: 1\napp:\n  web:\n    static: .\n    public: true\n  jobs:\n    image: forge-test-no-such-image:none\n"
	st := w.boot(t, "commit-a+digest", siteTree(t, contract, map[string]string{"index.html": "page"}))
	if st.Running || st.Error == "" || st.Phase == "" {
		t.Fatalf("a failed boot answered %+v", st)
	}
	for _, filter := range []string{"name=" + w.name, "label=forge.owner=" + w.stack} {
		if left := containersNamed(t, filter); left != "" {
			t.Fatalf("left by a failed boot (%s): %s", filter, left)
		}
	}
	if out, err := exec.Command("docker", "network", "ls", "-q", "--filter", "name="+docker.NetworkName(w.stack)).Output(); err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("the stack's network is still there: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(w.srv.State, "previews", w.name)); !os.IsNotExist(err) {
		t.Fatalf("a failed boot's tree is still on the worker: %v", err)
	}
}

// With real Docker: an ordinary change preview of a static site with a
// declared build (no deployment identity, no recorded images) is built,
// packed into the file server's image and run as that image, not a mount
// of the tree: it serves the built page and AppTown's /_apptown/env.js.
func TestIntegrationAWorkerPreviewBuildsAndServesADeclaredStaticSite(t *testing.T) {
	w := startPreviewWorker(t)
	contract := "version: 1\napp:\n  web:\n    static: { build: ., output: dist }\n    public: true\n"
	tree := siteTree(t, contract, map[string]string{
		"Dockerfile": "FROM scratch\nCOPY page.html /dist/index.html\n",
		"page.html":  "built page",
	})
	st := w.boot(t, "commit-a+digest", tree)
	if !st.Running || st.Error != "" {
		t.Fatalf("the boot answered %+v", st)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "image", "ls", "-q", "--filter", "reference=forge-preview-build-"+w.name+"*").Output(); err == nil {
			for _, id := range strings.Fields(string(out)) {
				_ = exec.Command("docker", "rmi", "-f", id).Run()
			}
		}
	})
	if got := w.served(t); got != "built page" {
		t.Fatalf("served %q", got)
	}
	mounts, err := exec.Command("docker", "inspect", "--format", "{{len .Mounts}} {{.Config.Image}}", st.ContainerID).Output()
	if err != nil || !strings.HasPrefix(string(mounts), "0 ") || strings.Contains(string(mounts), "caddy") {
		t.Fatalf("the preview is not the packed image: %q %v", mounts, err)
	}
	forge := httptest.NewServer(w.previews.Forward(w.name, "https"))
	defer forge.Close()
	resp, err := http.Get(forge.URL + "/_apptown/env.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "__APPTOWN_ENV__") {
		t.Fatalf("/_apptown/env.js: %d %q", resp.StatusCode, body)
	}
}

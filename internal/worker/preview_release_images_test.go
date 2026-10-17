package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
)

// A release's preview on the worker runs the release's images by digest:
// each is pulled by its digest, with the prepared registry login
// in the pull's own configuration, and nothing is built from the tree. A
// release preview naming an image other than by digest is refused before
// Docker is asked anything. (This Docker runs nothing; the boot's own
// result does not matter here.)
//
// Kills: a worker that ignores the spec's images and builds the tree; a
// pull without the prepared login; images by tag accepted.
func TestAReleasePreviewOnTheWorkerPullsItsImagesByDigestAndBuildsNothing(t *testing.T) {
	t.Parallel()
	dk, _ := sweepDocker(t)
	var mu sync.Mutex
	var loggedIn []string // the commands whose own configuration carried the login
	dk.reply = func(args []string) ([]byte, error, bool) {
		if len(args) >= 2 && args[0] == "--config" {
			if config, _ := os.ReadFile(filepath.Join(args[1], "config.json")); strings.Contains(string(config), "reg.example") {
				mu.Lock()
				loggedIn = append(loggedIn, strings.Join(args, " "))
				mu.Unlock()
			}
			args = args[2:]
		}
		line := strings.Join(args, " ")
		switch {
		case strings.Contains(line, "context show"):
			return []byte("default\n"), nil, true
		case strings.Contains(line, "{{.Id}} {{json .RepoDigests}}"):
			return []byte("sha256:pulled [\"" + args[len(args)-1] + "\"]\n"), nil, true
		case strings.Contains(line, "{{.State.Running}} {{.Id}}"):
			return []byte("Error: No such object\n"), errExit, true
		}
		return nil, nil, true
	}
	ca := workertest.NewCA(t)
	hs := workertest.StartServer(t, ca, &worker.Server{Installation: workertest.Installation, State: t.TempDir(), Previews: dk})
	previews := worker.Previews{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}

	c, err := appcontract.Parse([]byte("version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n  worker:\n    build: worker\ninit:\n  - name: migrate\n    build: .\n"))
	if err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	web := "reg.example/team/p@sha256:" + strings.Repeat("a", 64)
	work := "reg.example/team/p@sha256:" + strings.Repeat("b", 64)
	prepared := docker.Prepared{Version: docker.PreparedVersion, Contract: c, BrowserEnv: "{}",
		Registry: &docker.RegistryCredential{Host: "reg.example", Username: "robot", Token: "preview-token"}}
	spec := worker.PreviewSpec{Name: "forge-preview-p-release-3", StackID: "pvrel-p-3", Slug: "p", Incarnation: "bundle-1+digest", ProbeSeconds: 1,
		Images: &worker.PreviewImages{Parts: map[string]string{"web": "reg.example/team/p:latest", "worker": work}, Init: map[string]string{"migrate": web}}}
	if _, err := previews.Boot(context.Background(), spec, prepared, tree); err == nil || !strings.Contains(err.Error(), "by digest") {
		t.Fatalf("a release preview naming an image by tag: %v", err)
	}
	if asked := dk.commands(""); len(asked) != 0 {
		t.Fatalf("a refused boot asked Docker:\n%s", strings.Join(asked, "\n"))
	}

	spec.Images.Parts["web"] = web
	_, _ = previews.Boot(context.Background(), spec, prepared, tree)
	body := strings.Join(dk.commands(""), "\n")
	pulled := map[string]bool{}
	for _, line := range dk.commands("") {
		args := strings.Fields(line)
		if len(args) >= 2 && args[0] == "--config" {
			args = args[2:]
		}
		switch {
		case len(args) == 2 && args[0] == "pull":
			pulled[args[1]] = true
		case len(args) > 0 && (args[0] == "build" || args[0] == "buildx"):
			t.Errorf("the worker built: %s", line)
		}
	}
	if !pulled[web] || !pulled[work] || len(pulled) != 2 {
		t.Fatalf("pulled %v, want the release's two digests:\n%s", pulled, body)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, ref := range []string{web, work} {
		if !strings.Contains(strings.Join(loggedIn, "\n"), "pull "+ref) {
			t.Errorf("%s was not pulled with the prepared login:\n%s", ref, loggedIn)
		}
	}
}

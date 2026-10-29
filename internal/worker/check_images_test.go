package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/execfile"
	"github.com/flokkit/apptown/stock"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/worker"
	"github.com/flokkit/apptown/internal/worker/workertest"
)

// fakeImageDocker is the worker's Docker for images: it knows which tags it
// has (a file per tag), and records what it is asked, with the Docker
// configuration folder each command used.
func fakeImageDocker(t *testing.T) (binary, calls, have string) {
	t.Helper()
	dir := t.TempDir()
	calls, have = filepath.Join(dir, "calls"), filepath.Join(dir, "have")
	if err := os.MkdirAll(have, 0o755); err != nil {
		t.Fatal(err)
	}
	binary = filepath.Join(dir, "docker")
	script := `#!/bin/sh
config=""
if [ "$1" = "--config" ]; then config="$2"; shift 2; fi
echo "$@ config=$config" >> "` + calls + `"
case "$*" in inspect*OOMKilled*) echo false; exit 0;; esac
case "$*" in
"buildx version") echo "github.com/docker/buildx v0.38.1"; exit 0;;
"context show") echo default; exit 0;;
"buildx inspect default") printf 'Name: default\nDriver: docker\n'; exit 0;;
esac
case "$1" in
image)
  ref="$5"; name=$(echo "$ref" | tr '/:' '__')
  [ -f "` + have + `/$name" ] || { echo "Error: No such image: $5" >&2; exit 1; }
  echo "\"sha256:$name\" {\"forge.go.version\":\"1.24.4\"}"; exit 0;;
login) [ -n "$config" ] && [ -d "$config" ] || exit 9; cat > /dev/null; exit 0;;
pull) if [ -n "$config" ] && [ -f "$config/config.json" ]; then cat "$config/config.json" >> "` + calls + `"; echo >> "` + calls + `"; fi
  name=$(echo "$2" | tr '/:' '__'); touch "` + have + `/$name"; exit 0;;
build) tag=""; prev=""; for a in "$@"; do [ "$prev" = "-t" ] && tag="$a"; prev="$a"; done
  name=$(echo "$tag" | tr '/:' '__'); [ -f "$prev/Dockerfile" ] || exit 2; touch "` + have + `/$name"; exit 0;;
save) echo "image archive of $2"; exit 0;;
run) exit 0;;
esac
exit 0
`
	if err := execfile.Write(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, calls, have
}

// An image for a check is prepared on the worker and named by its ID: found
// by its tag when there, pulled with a login made for that one request, or
// built from the tree sent with it, only when building is allowed.
func TestACheckImageIsPreparedOnTheWorker(t *testing.T) {
	t.Parallel()
	binary, calls, _ := fakeImageDocker(t)
	ca := workertest.NewCA(t)
	hs := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(), Checks: execution.Runner{DockerBinary: binary}})
	checks := worker.Checks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	ctx := context.Background()
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "app", "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	build := execution.ImageRequest{Tag: "apptown-conform:abc", Tree: tree, Context: "app"}
	if a := checks.Image(ctx, build); !a.NotBuilt || a.ID != "" || a.Err != "" {
		t.Fatalf("a build that was not allowed: %+v", a)
	}
	build.AllowBuild = true
	if a := checks.Image(ctx, build); a.Err != "" || a.ID != "sha256:apptown-conform_abc" {
		t.Fatalf("a build on the worker: %+v", a)
	}
	if a := checks.Image(ctx, execution.ImageRequest{Tag: "apptown-conform:abc"}); a.ID != "sha256:apptown-conform_abc" || a.Labels["forge.go.version"] != "1.24.4" {
		t.Fatalf("a cached image, with its labels: %+v", a)
	}
	pull := execution.ImageRequest{Pull: "registry.example/app:1", Registry: &execution.RegistryLogin{Host: "registry.example", Username: "u", Token: "t"}}
	if a := checks.Image(ctx, pull); a.Err != "" || a.ID != "sha256:registry.example_app_1" {
		t.Fatalf("a private pull: %+v", a)
	}
	escape := execution.ImageRequest{Tag: "x", Tree: tree, Context: "../elsewhere", AllowBuild: true}
	if a := checks.Image(ctx, escape); a.Err == "" {
		t.Fatalf("a build context outside the tree: %+v", a)
	}
	recorded, _ := os.ReadFile(calls)
	var login, pulled string
	for _, line := range strings.Split(string(recorded), "\n") {
		if strings.HasPrefix(line, "login ") {
			login = line[strings.Index(line, "config=")+7:]
		}
		if strings.HasPrefix(line, "pull ") {
			pulled = line[strings.Index(line, "config=")+7:]
		}
	}
	if login == "" || login != pulled {
		t.Fatalf("the pull did not use the login's own configuration: %s", recorded)
	}
	if _, err := os.Stat(login); !os.IsNotExist(err) {
		t.Fatal("the request's Docker configuration outlived it")
	}
	// Built by the one build rule (the daemon's own builder named) and
	// labelled for the sweep.
	if !strings.Contains(string(recorded), "build --label apptown.built=1 --builder default --label apptown.class=check -t apptown-conform:abc") {
		t.Fatalf("a check image is not built by the rule and labelled for the sweep: %s", recorded)
	}
}

// An image scan exports the image into the tree where the scan runs, and an
// exec-image check runs with no tree mounted.
func TestAnImageIsScannedAndRunWhereItWasPrepared(t *testing.T) {
	t.Parallel()
	binary, calls, _ := fakeImageDocker(t)
	ca := workertest.NewCA(t)
	hs := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(), Checks: execution.Runner{DockerBinary: binary}})
	checks := worker.Checks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	scan := checks.Run(context.Background(), execution.Request{Workspace: t.TempDir(), Image: "trivy:test", Argv: []string{"image", "--input", "/work/.apptown/image.tar"},
		Network: "none", Timeout: time.Minute, Name: "s", ExportImage: "sha256:app"})
	inImage := checks.Run(context.Background(), execution.Request{Image: "sha256:app", Argv: []string{"python", "manage.py", "check"}, Env: []string{"A=1"},
		Network: "none", Timeout: time.Minute, Name: "e"})
	if !scan.Exited0() || !inImage.Exited0() {
		t.Fatalf("scan %+v, exec-image %+v", scan, inImage)
	}
	recorded, _ := os.ReadFile(calls)
	var saved bool
	for _, line := range strings.Split(string(recorded), "\n") {
		if strings.HasPrefix(line, "save sha256:app") {
			saved = true
		}
		if strings.HasPrefix(line, "run ") && strings.Contains(line, "sha256:app python") && strings.Contains(line, ":/work") {
			t.Fatalf("an exec-image check had a tree mounted: %s", line)
		}
	}
	if !saved {
		t.Fatalf("the image was not exported into the scan's tree: %s", recorded)
	}
}

// A check image goes a day after its last use, not its making: one used
// within the day stays, one idle for longer is removed, and one the sweep
// sees for the first time gets its day from then.
func TestACheckImageIsPrunedADayAfterItsLastUse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	binary := filepath.Join(dir, "docker")
	script := `#!/bin/sh
echo "$@" >> "` + calls + `"
case "$*" in inspect*OOMKilled*) echo false; exit 0;; esac
case "$*" in
"buildx version") echo "github.com/docker/buildx v0.38.1"; exit 0;;
"context show") echo default; exit 0;;
"buildx inspect default") printf 'Name: default\nDriver: docker\n'; exit 0;;
esac
case "$1" in
images) printf 'sha256:idle\nsha256:recent\nsha256:unseen\n';;
esac
exit 0
`
	if err := execfile.Write(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ca := workertest.NewCA(t)
	srv := &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(), Checks: execution.Runner{DockerBinary: binary}}
	hs := workertest.StartServer(t, ca, srv)
	checks := worker.Checks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	uses := filepath.Join(srv.State, "check-images")
	if err := os.MkdirAll(uses, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{"idle", "recent"} {
		if err := os.WriteFile(filepath.Join(uses, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(uses, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	// "recent" is used by a check now: an exec-image run names it.
	if out := checks.Run(context.Background(), execution.Request{Image: "sha256:recent", Argv: []string{"true"}, Network: "none", Timeout: time.Minute, Name: "e"}); !out.Exited0() {
		t.Fatalf("a run in the image: %+v", out)
	}
	srv.SweepChecks(context.Background(), docker.CLI{Binary: binary}, false)
	recorded, _ := os.ReadFile(calls)
	got := string(recorded)
	if !strings.Contains(got, "rmi sha256:idle") || strings.Contains(got, "rmi sha256:recent") || strings.Contains(got, "rmi sha256:unseen") {
		t.Fatalf("the sweep's removals:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(uses, "unseen")); err != nil {
		t.Fatal("an image seen for the first time was given no day")
	}
}

func TestAnonymousImagePreparationIsEnforcedByTheWorker(t *testing.T) {
	t.Setenv("DOCKER_AUTH_CONFIG", "")
	binary, calls, _ := fakeImageDocker(t)
	ca := workertest.NewCA(t)
	hs := workertest.StartServer(t, ca, &worker.Server{ToolVersions: stock.Versions(), Installation: workertest.Installation, State: t.TempDir(), Checks: execution.Runner{DockerBinary: binary}})
	checks := worker.Checks{URL: hs.URL, Client: workertest.Client(t, ca, workertest.Installation)}
	got := checks.Image(t.Context(), execution.ImageRequest{Pull: "registry.example/anonymous:1", Anonymous: true})
	if got.Err != "" || got.ID == "" {
		t.Fatalf("worker anonymous preparation: %+v", got)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "login ") || !strings.Contains(string(raw), `"auths":{"registry.example":{}}`) {
		t.Fatalf("worker lost anonymous mode: %s", raw)
	}
	var config string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "pull ") {
			_, config, _ = strings.Cut(line, "config=")
		}
	}
	if config == "" {
		t.Fatalf("worker pulled with ambient configuration: %s", raw)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("worker config not settled: %v", err)
	}
	t.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{"registry.example":{"auth":"dTpw"}}}`)
	refused := checks.Image(t.Context(), execution.ImageRequest{Pull: "registry.example/other:1", Anonymous: true})
	after, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if refused.Err == "" || string(after) != string(raw) {
		t.Fatalf("worker used environment auth: %+v %s", refused, after)
	}
}

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
)

// apptown dev writes its compose file under .apptown/, but the file's relative
// paths name the app's tree: compose must resolve them from the repo root.
// The layout is reading-list's (a Dockerfile at the root that copies a
// frontend/ subfolder), where compose run from .apptown/ failed with
// "open Dockerfile: no such file or directory".
func TestDevComposeResolvesPathsFromTheRepoRoot(t *testing.T) {
	args := devComposeArgs("up", "-d")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--project-directory . -f "+devComposePath) || args[len(args)-1] != "-d" {
		t.Fatalf("compose args: %v", args)
	}
	// Where a compose client is available, ask it where a relative build
	// context lands.
	if dockertest.Run("compose", "version") != nil {
		t.Skip("no docker compose client here; the argument contract above still holds")
	}
	root := t.TempDir()
	for _, dir := range []string{".apptown", "frontend"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		devComposePath:          "services:\n  web:\n    build: .\n",
		"Dockerfile":            "FROM scratch\nCOPY frontend/ /src/frontend/\n",
		"frontend/package.json": "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("docker", devComposeArgs("config", "--format", "json")...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("compose config: %v", err)
	}
	var cfg struct {
		Services map[string]struct {
			Build struct{ Context, Dockerfile string }
		}
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	// The temp root may be reached through a symlink (/var → /private/var).
	build := cfg.Services["web"].Build
	if filepath.Base(build.Context) != filepath.Base(root) {
		t.Fatalf("build context %q is not the repo root %s", build.Context, root)
	}
	// The build file compose will open is the repo's own Dockerfile.
	if _, err := os.Stat(filepath.Join(build.Context, build.Dockerfile)); err != nil {
		t.Fatalf("compose would build from %s/%s: %v", build.Context, build.Dockerfile, err)
	}
}

// In a session whose Docker is another machine, apptown dev up names that
// machine as where the app's ports answer: localhost never does. The
// machine is the one the Docker setting names, DOCKER_HOST or a context
// alike (execution.SelectDocker), and a setting that cannot be read is an
// error, not "this machine".
func TestDevUpNamesWherePublishedPortsAnswer(t *testing.T) {
	config := t.TempDir()
	sum := sha256.Sum256([]byte("remote"))
	dir := filepath.Join(config, "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"Name":"remote","Endpoints":{"docker":{"Host":"tcp://h:2375"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ host, context, want string }{
		{"tcp://dind:2375", "", "dind"},
		{"tcp://10.0.0.5:2376/", "", "10.0.0.5"},
		{"unix:///var/run/docker.sock", "", ""},
		{"", "", ""},
		{"", "remote", "h"},
	} {
		t.Setenv("DOCKER_HOST", tc.host)
		t.Setenv("DOCKER_CONTEXT", tc.context)
		t.Setenv("DOCKER_CONFIG", config)
		if got, err := publishedPortsHost(); err != nil || got != tc.want {
			t.Errorf("DOCKER_HOST=%q context %q: got %q %v, want %q", tc.host, tc.context, got, err, tc.want)
		}
	}
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "missing")
	if got, err := publishedPortsHost(); err == nil {
		t.Errorf("a context that cannot be read named %q", got)
	}
}

// `apptown dev up` builds each static site before compose, the way a deploy
// does (complaint #91): the build's stage exported into the site's folder,
// emptied first so nothing stale is served, and a build that wrote no output
// folder is refused rather than served empty.
func TestDevUpBuildsStaticSitesBeforeCompose(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	folder := ".apptown/static/web"
	if err := os.MkdirAll(folder+"/dist", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(folder+"/dist/stale.html", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var ran [][]string
	writes := func(args ...string) error {
		ran = append(ran, args)
		if _, err := os.Stat(folder + "/dist/stale.html"); err == nil {
			t.Error("the folder was not emptied before the build")
		}
		if err := os.MkdirAll(folder+"/dist", 0o755); err != nil {
			return err
		}
		return os.WriteFile(folder+"/dist/index.html", []byte("new"), 0o644)
	}
	builds := []devStaticBuild{{Part: "web", Context: ".", Target: "site", Output: "dist", Folder: folder}}
	if err := buildStaticSites(builds, writes); err != nil {
		t.Fatal(err)
	}
	want := []string{"build", "--target", "site", "--output", "type=local,dest=" + folder, "."}
	if len(ran) != 1 || !slices.Equal(ran[0], want) {
		t.Fatalf("ran %v, want %v", ran, want)
	}

	// The deploy path's confinement holds here too: the folder is mounted
	// into a container, so an output that is missing, or leads out of the
	// build through a link, never reaches compose.
	writesNothing := func(args ...string) error { return nil }
	if err := buildStaticSites(builds, writesNothing); err == nil || !strings.Contains(err.Error(), "wrote no dist") {
		t.Fatalf("a build without its output folder: %v", err)
	}
	outside := t.TempDir()
	linksOut := func(args ...string) error {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			return err
		}
		return os.Symlink(outside, folder+"/dist")
	}
	if err := buildStaticSites(builds, linksOut); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("an output that links out of the build: %v", err)
	}
	if got := staticParts(builds); !slices.Equal(got, []string{"web"}) {
		t.Fatalf("parts to recreate after the build: %v", got)
	}
}

// `apptown dev up` builds and checks every static site before compose runs,
// recreates the static sites' services after it, and runs compose not at
// all when a site's build is refused; an app with no static build runs
// exactly the compose it always did (complaint #91).
func TestDevUpRunsStaticBuildsBeforeComposeAndRecreatesTheirServices(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	builds := []devStaticBuild{{Part: "web", Context: ".", Output: "dist", Folder: ".apptown/static/web"}}
	record := func(writes bool) (*[]string, func(bool, ...string) error) {
		var ran []string
		return &ran, func(quiet bool, args ...string) error {
			ran = append(ran, strings.Join(args, " "))
			if writes && args[0] == "build" {
				if err := os.MkdirAll(".apptown/static/web/dist", 0o755); err != nil {
					return err
				}
				return os.WriteFile(".apptown/static/web/dist/index.html", []byte("x"), 0o644)
			}
			return nil
		}
	}
	compose := "compose --project-directory . -f " + devComposePath + " "

	ran, docker := record(true)
	if err := devUpStart(builds, docker); err != nil {
		t.Fatal(err)
	}
	want := []string{"build --output type=local,dest=.apptown/static/web .", "compose version", compose + "up -d --build", "compose version", compose + "up -d --no-deps --force-recreate web"}
	if !slices.Equal(*ran, want) {
		t.Fatalf("ran %q, want %q", *ran, want)
	}

	ran, docker = record(false)
	if err := devUpStart(builds, docker); err == nil {
		t.Fatal("a build that wrote no output still started compose")
	}
	for _, r := range *ran {
		if strings.HasPrefix(r, "compose") {
			t.Fatalf("compose ran after a refused build: %q", *ran)
		}
	}

	ran, docker = record(true)
	if err := devUpStart(nil, docker); err != nil {
		t.Fatal(err)
	}
	if want := []string{"compose version", compose + "up -d --build"}; !slices.Equal(*ran, want) {
		t.Fatalf("an app without static builds ran %q, want %q", *ran, want)
	}
}

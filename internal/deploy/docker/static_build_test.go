package docker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
)

// The exported stage is the project's own output: the site folder is
// served only when nothing on the way to it, or in it, is a link, so a
// build cannot make the apptown pack files from outside its own output.
// Each case names what the refusal must say.
func TestABuiltStaticSiteWithALinkIsRefused(t *testing.T) {
	t.Parallel()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("host file"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		output string
		make   func(root string) error
		want   string
	}{
		"the output folder is an absolute link": {"dist", func(root string) error {
			return os.Symlink(outside, filepath.Join(root, "dist"))
		}, "dist is a symbolic link"},
		"a folder on the way is a link": {"app/dist", func(root string) error {
			if err := os.MkdirAll(filepath.Join(outside, "dist"), 0o755); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(root, "app"))
		}, "app is a symbolic link"},
		"a link inside leads out": {"dist", func(root string) error {
			if err := writeFile(root, "dist/index.html"); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "dist", "assets.txt"))
		}, "dist/assets.txt, which is not a plain folder or file"},
		"a relative link inside": {"dist", func(root string) error {
			if err := writeFile(root, "dist/index.html"); err != nil {
				return err
			}
			return os.Symlink("index.html", filepath.Join(root, "dist", "home.html"))
		}, "dist/home.html"},
		"no output folder":     {"dist", func(root string) error { return writeFile(root, "build/index.html") }, "the build wrote no dist"},
		"the output is a file": {"dist", func(root string) error { return writeFile(root, "dist") }, "dist is not a folder"},
		"an empty site":        {"dist", func(root string) error { return os.MkdirAll(filepath.Join(root, "dist", "assets"), 0o755) }, "holds no files"},
	} {
		root := t.TempDir()
		if err := tc.make(root); err != nil {
			t.Fatal(err)
		}
		if site, err := ConfineStaticExport(root, tc.output); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %q %v, want %q", name, site, err, tc.want)
		}
	}
	root := t.TempDir()
	if err := writeFile(root, "dist/index.html"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(root, "dist/assets/app.js"); err != nil {
		t.Fatal(err)
	}
	// Links elsewhere in the stage are never read (a full image's /bin).
	if err := os.Symlink(outside, filepath.Join(root, "bin")); err != nil {
		t.Fatal(err)
	}
	if site, err := ConfineStaticExport(root, "./dist"); err != nil || site != filepath.Join(root, "dist") {
		t.Fatalf("a plain site: %q %v", site, err)
	}
	if site, err := ConfineStaticExport(filepath.Join(root, "dist"), "."); err != nil || site != filepath.Join(root, "dist") {
		t.Fatalf("the whole stage as the site: %q %v", site, err)
	}
}

func writeFile(root, rel string) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, rel), []byte("x"), 0o644)
}

// A boot's own Docker configuration still finds the CLI's plugins where
// the ambient configuration keeps them (buildx: a static site's build
// exports its files with it; Docker Desktop keeps it there), and reads
// nothing else of that configuration.
func TestABootsDockerConfigurationFindsTheCLIPlugins(t *testing.T) {
	ambient := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ambient, "cli-plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambient, "config.json"), []byte(`{"auths":{"ambient.example":{"auth":"c2VjcmV0"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", ambient)
	dk, done, err := (Scope{Client: &fakeClient{}}).WithHost("ssh://attached", "attached", "").WithBootConfig(t.Context(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	raw, err := os.ReadFile(filepath.Join(dk.Config, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Auths   map[string]any `json:"auths"`
		Plugins []string       `json:"cliPluginsExtraDirs"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Plugins) != 1 || config.Plugins[0] != filepath.Join(ambient, "cli-plugins") || config.Auths["ambient.example"] != nil {
		t.Fatalf("the boot's configuration: %s", raw)
	}
}

// A standing deployment of a static site with a declared build is built
// and packed on its daemon (PrepareImages): the site's stage is exported,
// then packed into the file server's image, and the part runs that image
// by ID as a static site. A release's recorded images are never built.
func TestAStandingStaticBuildIsPackedAndRunAsAnImage(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web:\n    public: true\n    static: { build: site, target: out, output: dist }\n"))
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := writeFile(ws, "site/Dockerfile"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeClient{}
	fake.reply = func(req Command) ([]byte, error) {
		for _, a := range req.Args {
			if dest, ok := strings.CutPrefix(a, "type=local,dest="); ok {
				return nil, writeFile(dest, "dist/index.html")
			}
		}
		if req.Args[0] == "image" {
			return []byte("sha256:packed"), nil
		}
		return nil, nil
	}
	req := BootRequest{Name: "forge-env-p-main", Workspace: ws, Docker: Scope{Client: fake},
		Identity: &deploy.DockerIdentity{Deployment: "1", Generation: 2, Artifact: "a"}}
	plan, err := NewHost(Dependencies{}).PrepareImages(t.Context(), req, appcontract.Prepare(c, nil))
	if err != nil {
		t.Fatal(err)
	}
	pub := plan.PublicPart()
	if pub.Image != "sha256:packed" || !pub.ServesStatic || pub.StaticBuild != nil || len(pub.Command) != 0 {
		t.Fatalf("the prepared part: %+v", pub)
	}
	var builds []string
	for _, args := range fake.args {
		if args[0] == "build" {
			builds = append(builds, strings.Join(args, " "))
		}
	}
	if len(builds) != 2 || !strings.HasPrefix(builds[0], "build --target out --output type=local,dest=") || !strings.HasSuffix(builds[0], filepath.Join(ws, "site")) ||
		!strings.Contains(builds[1], "-f ") || !strings.HasSuffix(builds[1], "/dist") {
		t.Fatalf("export then pack: %q", builds)
	}

	fake.args = nil
	recorded := &RecordedImages{Parts: map[string]string{"web": "sha256:recorded"}, SelfServing: true}
	req.Recorded = recorded
	fake.reply = func(req Command) ([]byte, error) { return []byte("sha256:recorded"), nil }
	if _, err := NewHost(Dependencies{}).PrepareImages(t.Context(), req, appcontract.Prepare(c, nil)); err != nil {
		t.Fatal(err)
	}
	for _, args := range fake.args {
		if args[0] == "build" {
			t.Fatalf("a recorded release was built: %v", args)
		}
	}
}

package docker

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestDockerSelectedImagesUseLocalAndRemoteBuildContexts(t *testing.T) {
	t.Parallel()
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprint(remote), func(t *testing.T) {
			fake := &fakeClient{}
			repo := selectedSourceRepo(t, map[string]string{
				".apptown/app.yaml": "app:\n  web:\n    public: true\n    static: site\n  mirror:\n    static: site\n    ports: [\"9090\"]\n  worker:\n    build: worker\ninit:\n  - name: migrate\n    build: migrations\n",
				"site/index.html":   "selected A", "worker/Dockerfile": "FROM scratch\n", "migrations/Dockerfile": "FROM scratch\n",
			})
			art, cleanup, err := PrepareSource(context.Background(), repo, "HEAD", "p", []deploy.ServiceSpec{{Name: "selected-service", Image: "example/service:1"}})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if err := os.WriteFile(filepath.Join(repo, "site/index.html"), []byte("moving B"), 0o644); err != nil {
				t.Fatal(err)
			}
			host := ""
			if remote {
				host = "ssh://selected-host"
			}
			dk := (Scope{Client: fake}).WithHost(host, "selected-host", "")
			var builds int
			staticImages := map[string]bool{}
			fake.reply = func(req Command) ([]byte, error) {
				args := req.Args
				if args[0] == "build" {
					builds++
					// A static site's image says how it is served, on its
					// own port: the same folder on two ports is two images.
					if i := slices.Index(args, "-f"); i > 0 {
						raw, err := os.ReadFile(args[i+1])
						if err != nil || !strings.Contains(string(raw), "\nCMD [\"sh\",\"-c\",") {
							t.Fatalf("a static image without its own command: %q %v", raw, err)
						}
						staticImages[string(raw)] = true
					}
					root := args[len(args)-1]
					if !strings.HasPrefix(root, art.Source.Workspace+string(filepath.Separator)) {
						t.Fatalf("build escaped selected source: %s", root)
					}
					if filepath.Base(root) == "site" {
						raw, err := os.ReadFile(filepath.Join(root, "index.html"))
						if err != nil || string(raw) != "selected A" {
							t.Fatalf("wrong static bytes %q %v", raw, err)
						}
					}
					if !strings.Contains(args[2], "-g7-") {
						t.Fatalf("shared generation tag: %v", args)
					}
				}
				if args[0] == "image" {
					return []byte("sha256:built\n"), nil
				}
				return nil, nil
			}
			got, err := NewHost(Dependencies{}).PrepareImages(t.Context(), BootRequest{Name: "apptown-env-p-main", Workspace: art.Source.Workspace, Docker: dk, Identity: &deploy.DockerIdentity{Deployment: "1", Generation: 7, Artifact: art.Hash}}, appcontract.Prepare(art.Source.Contract, nil))
			if err != nil {
				t.Fatal(err)
			}
			for _, calledHost := range fake.hosts {
				if calledHost != host {
					t.Fatalf("wrong daemon %q", calledHost)
				}
			}
			if builds != 4 || len(staticImages) != 2 {
				t.Fatalf("build each selected context, got %d builds, %d static images", builds, len(staticImages))
			}
			for _, p := range got.Parts {
				if p.Image != "sha256:built" || p.Build != "" || p.Static != "" {
					t.Fatalf("unprepared part %+v", p)
				}
			}
			if got.Init[0].Image != "sha256:built" || got.Init[0].Build != "" {
				t.Fatalf("unprepared init %+v", got.Init)
			}
			if art.Source.Contract.PublicPart().Static != "site" {
				t.Fatal("image preparation mutated selected contract")
			}
			restored, remove, err := RestoreSource(context.Background(), repo, "p", string(art.Body))
			if err != nil {
				t.Fatal(err)
			}
			defer remove()
			if restored.Hash != art.Hash || len(SourceServices(restored)) != 1 {
				t.Fatal("rebuild lost original service overrides")
			}
		})
	}
}

// A release's recorded images are booted as they are, on the daemon that
// holds them: nothing is built or pulled, a static part still becomes the
// file server, init steps and image-only parts keep their recorded IDs, and
// an image the daemon no longer has is missing, while a daemon that cannot
// be asked is not (design/operations/workers.md, "Running AppTown without a
// local Docker daemon").
func TestRecordedImagesBootAsRecordedAndBuildNothing(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n  worker:\n    build: worker\n  cache:\n    image: example/cache:7\ninit:\n  - name: migrate\n    build: migrations\n  - name: seed\n    image: example/seed:1\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"present", "missing", "unreachable"} {
		t.Run(scenario, func(t *testing.T) {
			recorded := &RecordedImages{
				Parts: map[string]string{"web": "sha256:web", "worker": "sha256:worker", "cache": "sha256:cache"},
				Init:  map[string]string{"migrate": "sha256:migrate", "seed": "sha256:seed"},
			}
			fake := &fakeClient{}
			fake.reply = func(req Command) ([]byte, error) {
				args := req.Args
				if args[0] != "image" {
					return nil, nil
				}
				id := args[len(args)-1]
				switch {
				case scenario == "missing" && id == "sha256:seed":
					return []byte("Error response from daemon: No such image: " + id), errors.New("exit status 1")
				case scenario == "unreachable":
					return []byte("Cannot connect to the Docker daemon"), errors.New("exit status 1")
				}
				return []byte(id + "\n"), nil
			}
			dk := (Scope{Client: fake}).WithHost("ssh://attached", "attached", "")
			req := BootRequest{Name: "apptown-env-p-main", Workspace: t.TempDir(), Docker: dk, Recorded: recorded,
				Identity: &deploy.DockerIdentity{Deployment: "1", Generation: 2, Artifact: "a"}}
			got, err := NewHost(Dependencies{}).PrepareImages(t.Context(), req, appcontract.Prepare(c, nil))
			present := recorded.Present(context.Background(), dk)
			for _, args := range fake.args {
				if args[0] == "build" || args[0] == "pull" {
					t.Fatalf("a recorded release built or pulled: %v", args)
				}
			}
			for _, h := range fake.hosts {
				if h != "ssh://attached" {
					t.Fatalf("asked another daemon: %q", h)
				}
			}
			switch scenario {
			case "missing":
				if !errors.Is(err, ErrRecordedImageMissing) || !errors.Is(present, ErrRecordedImageMissing) || !strings.Contains(err.Error(), "sha256:seed") {
					t.Fatalf("a missing image: %v / %v", err, present)
				}
				return
			case "unreachable":
				if err == nil || errors.Is(err, ErrRecordedImageMissing) || present == nil || errors.Is(present, ErrRecordedImageMissing) {
					t.Fatalf("an unreachable daemon is not a missing image: %v / %v", err, present)
				}
				return
			}
			if err != nil || present != nil {
				t.Fatal(err, present)
			}
			for _, p := range got.Parts {
				if p.Image != recorded.Parts[p.Name] || p.Build != "" || p.Static != "" {
					t.Fatalf("part %s is not its recorded image: %+v", p.Name, p)
				}
			}
			web := got.PublicPart()
			// A release's static image carries its own command
			// (StaticSiteDockerfile): the host adds none. It is still a
			// static site.
			if !web.ServesStatic || len(web.Command) != 0 {
				t.Fatalf("the static part's command: %+v", web)
			}
			for _, it := range got.Init {
				if it.Image != recorded.Init[it.Name] || it.Build != "" {
					t.Fatalf("init %s is not its recorded image: %+v", it.Name, it)
				}
			}
		})
	}
}

// A release's images by digest are pulled on the daemon, in a Docker
// configuration of the pull's own that holds the release registry's
// credential and is gone after it, and are run as the ID the daemon holds
// them under once its RepoDigests confirm the digest. Nothing is built. A
// daemon holding the image under another digest is an error; a digest the
// registry answers it no longer has is missing (land again); a refused
// credential or an unreachable registry is an error, never missing.
func TestImagesByDigestArePulledWithTheirCredentialAndConfirmed(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  web:\n    public: true\n    static: site\n    ports: [\"8080\"]\n  worker:\n    build: worker\ninit:\n  - name: migrate\n    build: migrations\n"))
	if err != nil {
		t.Fatal(err)
	}
	digest := func(b byte) string { return "registry.test/team/p@sha256:" + strings.Repeat(string(b), 64) }
	recorded := &RecordedImages{Parts: map[string]string{"web": digest('a'), "worker": digest('b')}, Init: map[string]string{"migrate": digest('c')},
		Login: &RegistryCredential{Host: "registry.test", Username: "robot", Token: "token"}}
	for _, scenario := range []string{"held", "another digest", "manifest unknown", "unauthorized", "unreachable"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			recorded := *recorded
			recorded.Scratch = t.TempDir()
			var configs []string
			fake := &fakeClient{}
			fake.reply = func(req Command) ([]byte, error) {
				args := req.Args
				if len(args) >= 2 && args[0] == "--config" {
					raw, err := os.ReadFile(filepath.Join(args[1], "config.json"))
					if err != nil {
						t.Errorf("the pull's configuration: %v", err)
					}
					configs = append(configs, string(raw))
					args = args[2:]
				}
				switch {
				case args[0] == "pull" && args[1] == digest('b') && scenario == "manifest unknown":
					return []byte("Error response from daemon: manifest for " + args[1] + " not found: manifest unknown: manifest unknown"), errors.New("exit status 1")
				case args[0] == "pull" && scenario == "unauthorized":
					return []byte("Error response from daemon: unauthorized: authentication required"), errors.New("exit status 1")
				case args[0] == "pull" && scenario == "unreachable":
					return []byte("Error response from daemon: Get \"https://registry.test/v2/\": dial tcp: connection refused"), errors.New("exit status 1")
				case args[0] == "image" && strings.Contains(strings.Join(args, " "), "RepoDigests"):
					ref := args[len(args)-1]
					if scenario == "another digest" && ref == digest('c') {
						ref = digest('d')
					}
					return []byte(`sha256:id-` + ref[len(ref)-1:] + ` ["` + ref + `"]`), nil
				}
				return nil, nil
			}
			dk := (Scope{Client: fake}).WithHost("ssh://attached", "attached", "")
			req := BootRequest{Name: "apptown-env-p-main", Workspace: t.TempDir(), Docker: dk, Recorded: &recorded,
				Identity: &deploy.DockerIdentity{Deployment: "1", Generation: 2, Artifact: "a"}}
			got, err := NewHost(Dependencies{}).PrepareImages(t.Context(), req, appcontract.Prepare(c, nil))
			for _, args := range fake.args {
				if slices.Contains(args, "build") || slices.Contains(args, "buildx") {
					t.Fatalf("an image by digest was built: %v", args)
				}
			}
			for _, config := range configs {
				if !strings.Contains(config, `"registry.test"`) || !strings.Contains(config, base64.StdEncoding.EncodeToString([]byte("robot:token"))) {
					t.Errorf("a pull's configuration does not hold the registry's credential: %s", config)
				}
			}
			if left, _ := filepath.Glob(filepath.Join(recorded.Scratch, "docker-config-*")); len(left) != 0 {
				t.Errorf("a pull's configuration outlived it: %v", left)
			}
			switch scenario {
			case "held":
				if err != nil || len(configs) != 3 {
					t.Fatalf("%v, %d pulls with their own configuration", err, len(configs))
				}
				if got.PublicPart().Image != "sha256:id-a" || got.Init[0].Image != "sha256:id-c" || !got.PublicPart().ServesStatic {
					t.Fatalf("the images run: %+v", got)
				}
			case "manifest unknown":
				if !errors.Is(err, ErrRecordedImageMissing) || !strings.Contains(err.Error(), "land the change again") {
					t.Fatalf("a digest the registry no longer has: %v", err)
				}
			default:
				if err == nil || errors.Is(err, ErrRecordedImageMissing) {
					t.Fatalf("%s: %v, want an error that is not a missing image", scenario, err)
				}
			}
		})
	}
}

package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dockerconfig "github.com/docker/cli/cli/config"
	"github.com/flokkit/apptown/internal/execfile"
)

// noBuild is the build of a preparation that must build nothing.
func noBuild(t *testing.T) Build {
	return func(context.Context, ...string) ([]byte, error) {
		t.Error("a preparation that builds nothing built")
		return nil, errors.New("no build expected")
	}
}

func TestAnUnavailableDockerIsNotACacheMiss(t *testing.T) {
	a := PrepareImage(context.Background(), func(context.Context, io.Reader, ...string) ([]byte, error) {
		return []byte("Cannot connect to the Docker daemon"), errors.New("exit 1")
	}, noBuild(t), ImageRequest{Tag: "forge-conform:abc", Context: ".", AllowBuild: false})
	if a.Err == "" || a.NotBuilt {
		t.Fatalf("unavailable Docker was answered as an ordinary cache miss: %+v", a)
	}
}

// A login's Docker configuration for one request selects the same Docker as
// the ambient configuration: the context in use carries over, so the pull
// lands where the check runs, and the folder is made under the scratch the
// caller sweeps.
func TestARequestsLoginUsesTheSameDocker(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	scratch := t.TempDir()
	var configs []string
	cli := func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		switch {
		case args[0] == "context":
			return []byte("desktop-linux\n"), nil
		case args[0] == "--config":
			configs = append(configs, args[1])
			if args[2] == "pull" {
				body, err := os.ReadFile(filepath.Join(args[1], "config.json"))
				if err != nil || !strings.Contains(string(body), `"currentContext":"desktop-linux"`) {
					t.Errorf("the pull's configuration does not select the context in use: %s %v", body, err)
				}
				if link, err := os.Readlink(filepath.Join(args[1], "contexts")); err != nil || link != filepath.Join(os.Getenv("DOCKER_CONFIG"), "contexts") {
					t.Errorf("the context's records are not shared: %q %v", link, err)
				}
				if !strings.HasPrefix(args[1], scratch) {
					t.Errorf("the configuration is outside the request's scratch: %s", args[1])
				}
			}
			return nil, nil
		case args[0] == "image":
			if strings.HasSuffix(args[len(args)-1], "app:1") && len(configs) == 2 {
				return []byte(`"sha256:app" {}`), nil
			}
			return nil, errors.New("Error: No such image")
		}
		return nil, nil
	}
	a := PrepareImage(context.Background(), cli, noBuild(t), ImageRequest{Pull: "registry.example/app:1", Scratch: scratch,
		Registry: &RegistryLogin{Host: "registry.example", Username: "u", Token: "t"}})
	if a.ID != "sha256:app" || len(configs) != 2 || configs[0] != configs[1] {
		t.Fatalf("a private pull: %+v %v", a, configs)
	}
	if _, err := os.Stat(configs[0]); !os.IsNotExist(err) {
		t.Fatal("the request's configuration outlived it")
	}
}

// An image is exported into the tree only: neither a linked .apptown nor a
// linked image.tar in a real .apptown chooses where the archive is written.
func TestAnImageExportNeverFollowsALink(t *testing.T) {
	t.Parallel()
	docker := filepath.Join(t.TempDir(), "docker")
	if err := execfile.Write(docker, []byte("#!/bin/sh\n[ \"$1\" = save ] && echo archive\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, plant := range map[string]func(tree, outside string) error{
		"a linked .apptown": func(tree, outside string) error {
			return os.Symlink(filepath.Dir(outside), filepath.Join(tree, ".apptown"))
		},
		"a linked image.tar": func(tree, outside string) error {
			if err := os.Mkdir(filepath.Join(tree, ".apptown"), 0o755); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(tree, ".apptown", "image.tar"))
		},
	} {
		tree, outside := t.TempDir(), filepath.Join(t.TempDir(), "image.tar")
		if err := os.WriteFile(outside, []byte("untouched"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := plant(tree, outside); err != nil {
			t.Fatal(err)
		}
		out := Runner{DockerBinary: docker}.Run(context.Background(), Docker, Request{Workspace: tree, Image: "scanner:1", Argv: []string{"scan"},
			Timeout: 10 * time.Second, Name: "export", ExportImage: "sha256:app"})
		if body, _ := os.ReadFile(outside); string(body) != "untouched" || out.Termination != LaunchFailed {
			t.Errorf("%s: the export wrote %q outside the tree (%+v)", name, body, out)
		}
	}
}

func TestAnonymousImagePreparationDoesNotUseAmbientCredentials(t *testing.T) {
	t.Setenv("DOCKER_AUTH_CONFIG", "")
	ambient, scratch, bin := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("DOCKER_CONFIG", ambient)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	marker := filepath.Join(bin, "helper-used")
	for _, helper := range []string{"pass", "secretservice", "osxkeychain", "wincred"} {
		if err := execfile.Write(filepath.Join(bin, "docker-credential-"+helper), []byte("#!/bin/sh\necho used >> '"+marker+"'\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ambient, "config.json"), []byte(`{"credsStore":"osxkeychain","credHelpers":{"registry.example":"pass"},"auths":{"registry.example":{"auth":"dTpw"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	previous := dockerconfig.Dir()
	t.Cleanup(func() { dockerconfig.SetDir(previous) })
	var owned string
	pulled := false
	cli := func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		switch args[0] {
		case "context":
			return []byte("selected-daemon\n"), nil
		case "--config":
			if len(args) != 4 || args[2] != "pull" {
				t.Fatalf("unexpected authenticated command: %v", args)
			}
			owned = args[1]
			if !strings.HasPrefix(owned, scratch+string(os.PathSeparator)) {
				t.Fatalf("unowned configuration %s", owned)
			}
			dockerconfig.SetDir(owned)
			cfg := dockerconfig.LoadDefaultConfigFile(io.Discard)
			if cfg.CurrentContext != "selected-daemon" || cfg.CredentialsStore != "" || len(cfg.CredentialHelpers) != 0 || !cfg.ContainsAuth() {
				t.Fatalf("anonymous context: %+v", cfg)
			}
			for _, host := range []string{"registry.example", "https://index.docker.io/v1/"} {
				auth, err := cfg.GetAuthConfig(host)
				if err != nil || auth.Username != "" || auth.Password != "" || auth.IdentityToken != "" || auth.RegistryToken != "" {
					t.Fatalf("ambient authority reached pull: %+v %v", auth, err)
				}
			}
			link, err := os.Readlink(filepath.Join(owned, "contexts"))
			if err != nil || link != filepath.Join(ambient, "contexts") {
				t.Fatalf("changed daemon records: %s %v", link, err)
			}
			pulled = true
			return nil, nil
		case "image":
			if !pulled {
				return nil, errors.New("No such image")
			}
			return []byte(`"sha256:prepared" {}`), nil
		default:
			t.Fatalf("unexpected command %v", args)
			return nil, nil
		}
	}
	got := PrepareImage(t.Context(), cli, noBuild(t), ImageRequest{Pull: "registry.example/app@sha256:" + strings.Repeat("a", 64), Anonymous: true, Scratch: scratch})
	if got.Err != "" || got.ID != "sha256:prepared" || !pulled {
		t.Fatalf("anonymous preparation: %+v", got)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("configuration not settled: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("native credential helper ran: %v", err)
	}
}

func TestAnonymousImagePreparationRefusesSuppliedAndEnvironmentAuthority(t *testing.T) {
	cli := func(context.Context, io.Reader, ...string) ([]byte, error) {
		t.Fatal("refusal invoked Docker")
		return nil, nil
	}
	t.Setenv("DOCKER_AUTH_CONFIG", "")
	if got := PrepareImage(t.Context(), cli, noBuild(t), ImageRequest{Anonymous: true, Registry: &RegistryLogin{Token: "secret"}}); got.Err == "" {
		t.Fatal("mixed authority accepted")
	}
	t.Setenv("DOCKER_AUTH_CONFIG", `{"auths":{"registry.example":{"auth":"dTpw"}}}`)
	if got := PrepareImage(t.Context(), cli, noBuild(t), ImageRequest{Anonymous: true, Pull: "registry.example/app:1"}); got.Err == "" || strings.Contains(got.Err, "dTpw") {
		t.Fatalf("environment authority: %+v", got)
	}
}

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

	"flokkit.io/forge/internal/execfile"
)

func TestAnUnavailableDockerIsNotACacheMiss(t *testing.T) {
	a := PrepareImage(context.Background(), func(context.Context, io.Reader, ...string) ([]byte, error) {
		return []byte("Cannot connect to the Docker daemon"), errors.New("exit 1")
	}, ImageRequest{Tag: "forge-conform:abc", Context: ".", AllowBuild: false})
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
	a := PrepareImage(context.Background(), cli, ImageRequest{Pull: "registry.example/app:1", Scratch: scratch,
		Registry: &RegistryLogin{Host: "registry.example", Username: "u", Token: "t"}})
	if a.ID != "sha256:app" || len(configs) != 2 || configs[0] != configs[1] {
		t.Fatalf("a private pull: %+v %v", a, configs)
	}
	if _, err := os.Stat(configs[0]); !os.IsNotExist(err) {
		t.Fatal("the request's configuration outlived it")
	}
}

// An image is exported into the tree only: neither a linked .forge nor a
// linked image.tar in a real .forge chooses where the archive is written.
func TestAnImageExportNeverFollowsALink(t *testing.T) {
	t.Parallel()
	docker := filepath.Join(t.TempDir(), "docker")
	if err := execfile.Write(docker, []byte("#!/bin/sh\n[ \"$1\" = save ] && echo archive\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, plant := range map[string]func(tree, outside string) error{
		"a linked .forge": func(tree, outside string) error {
			return os.Symlink(filepath.Dir(outside), filepath.Join(tree, ".forge"))
		},
		"a linked image.tar": func(tree, outside string) error {
			if err := os.Mkdir(filepath.Join(tree, ".forge"), 0o755); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(tree, ".forge", "image.tar"))
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

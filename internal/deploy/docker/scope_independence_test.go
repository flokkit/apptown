package docker_test

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

type scopeClientFunc func(context.Context, string, docker.Command) ([]byte, error)

func (f scopeClientFunc) Run(ctx context.Context, host string, req docker.Command) ([]byte, error) {
	return f(ctx, host, req)
}

func TestDockerScopesStayIndependent(t *testing.T) {
	t.Parallel()
	daemons := map[string]*dockerfake.Daemon{"": dockerfake.New(), "ssh://one": dockerfake.New(), "ssh://two": dockerfake.New()}
	client := scopeClientFunc(func(ctx context.Context, host string, req docker.Command) ([]byte, error) {
		f, ok := daemons[host]
		if !ok {
			return nil, fmt.Errorf("unexpected daemon %q", host)
		}
		out, err := f.Run(ctx, host, req)
		if err != nil {
			return out, err
		}
		if req.Stdout != nil {
			_, err = io.WriteString(req.Stdout, "archive:"+host)
			return nil, err
		}
		if req.Stdin != nil {
			raw, err := io.ReadAll(req.Stdin)
			if err != nil {
				return nil, err
			}
			if string(raw) != "archive:"+host {
				return nil, fmt.Errorf("restore crossed daemons: %q on %q", raw, host)
			}
		}
		if req.Timeout != 0 && req.Timeout != 30*time.Second {
			return nil, fmt.Errorf("inspection lost its bound: %v", req.Timeout)
		}
		return out, nil
	})
	root := docker.Scope{Client: client}
	for host, f := range daemons {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			dk := root.WithHost(host, "host.example", "apps.example")
			network, cleanup, err := docker.StartServices(t.Context(), dk, "owned", []deploy.ServiceSpec{{Name: "db", Image: "db:1"}})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if network != docker.Current.Network("owned") {
				t.Fatal(network)
			}
			if _, err := dk.Run(context.Background(), "run", "--name", "app", "app:1"); err != nil {
				t.Fatal(err)
			}
			if !dk.Running(context.Background(), "app") {
				t.Fatal("started app not visible on selected daemon")
			}
			if _, err := docker.ContainerHostPort(context.Background(), dk, "app", 8080); err != nil {
				t.Fatal(err)
			}
			runtime, err := docker.Inspect(context.Background(), dk, "app")
			if err != nil || !runtime.Running {
				t.Fatalf("bounded observation: %+v %v", runtime, err)
			}
			path := filepath.Join(t.TempDir(), "backup")
			if err := dk.ToFile(context.Background(), path, "exec", "db", "dump"); err != nil {
				t.Fatal(err)
			}
			if _, err := dk.FromFile(context.Background(), path, "exec", "-i", "db", "restore"); err != nil {
				t.Fatal(err)
			}
			dk.ImageDigest(context.Background(), "app:1")
			dk.Remove(context.Background(), "app")
			cleanup()
			for _, part := range []string{"network create", "run ", "inspect ", "exec db dump", "exec -i db restore", "image inspect", "rm -f app", "network rm"} {
				if len(f.Matching(part)) == 0 {
					t.Fatalf("daemon %q missed %q: %v", host, part, f.Commands())
				}
			}
		})
	}
}

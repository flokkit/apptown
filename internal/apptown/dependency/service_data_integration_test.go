//go:build integration

package dependency

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockertest"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/stock"
)

// Every stock kind whose emulator keeps data starts through the tests
// step's runner, as the runner starts it, and a command beside it reaches
// it on its port: S3's emulator exited at once there ("chdir /data: no such file or
// directory") because nothing gave it its data folder. A kind added with a
// data path its image does not make itself fails here, not in a project.
func TestIntegrationEveryEmulatorThatKeepsDataStartsBesideATestRun(t *testing.T) {
	files, err := fs.Glob(stock.FS, "dependencies/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	started := 0
	for _, f := range files {
		raw, err := stock.FS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		kind, err := Parse(string(raw))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		local := kind.ResolveLocal(nil, "", nil)
		if local == nil || local.Data == "" {
			continue
		}
		if local.Port == 0 {
			t.Fatalf("%s keeps data but declares no port to reach it on", local.Name)
		}
		started++
		t.Run(path.Base(f), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			out := (execution.Runner{}).Run(ctx, execution.Docker, execution.Request{
				// The command reaches the emulator on its port, so the test
				// witnesses it serving, not merely running at one poll.
				Argv:  []string{"sh", "-c", fmt.Sprintf("for i in $(seq 1 60); do nc -w 1 %s %d </dev/null && exit 0; sleep 1; done; exit 1", local.Name, local.Port)},
				Image: "busybox:latest", Network: "none",
				Name:     execution.UniqueName("forge-test-data-" + local.Name),
				Timeout:  4 * time.Minute,
				Services: []deploy.ServiceSpec{local.Spec()},
			})
			if !out.Exited0() || out.RunErr != nil {
				t.Fatalf("%s's emulator did not start beside a run: %+v", local.Name, out)
			}
		})
	}
	if started == 0 {
		t.Fatal("no stock kind keeps data: the test checked nothing")
	}
}

// A standing S3 store made by the s3.yaml of before 17 October 2026 (the
// gateway's root key local/localsecret given to the app, no bucket made)
// holding an object in a bucket the app made, on a deployment with real
// data, takes the start script on its own volume: the release remakes it
// rather than refusing, and the app's key (still local/localsecret) reads
// the object and can no longer make a bucket.
func TestIntegrationAnOldS3StoreTakesTheStartScriptOnItsVolume(t *testing.T) {
	raw, err := stock.FS.ReadFile("dependencies/s3.yaml")
	if err != nil {
		t.Fatal(err)
	}
	kind, err := Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	current := kind.ResolveLocal(nil, "", nil).Spec()
	old := deploy.ServiceSpec{Name: "s3", Image: current.Image, Command: []string{"--port", ":9000", "posix", "/data"},
		Env: map[string]string{"ROOT_ACCESS_KEY_ID": "local", "ROOT_SECRET_ACCESS_KEY": "localsecret"}, Data: "/data"}
	stack := fmt.Sprintf("env-oldstore%d-main", os.Getpid())
	dk := docker.Scope{Client: docker.CLI{Binary: "docker"}}
	t.Cleanup(func() {
		_ = dockertest.Run("rm", "-f", docker.Current.Service(stack, "s3"))
		_ = dockertest.Run("volume", "rm", docker.Current.NeedVolume(stack, "s3"))
		_ = dockertest.Run("network", "rm", docker.Current.Network(stack))
	})
	own := docker.Ownership{Record: func(deploy.Owned) error { return nil }, Needs: map[string]string{"s3": "/data"}}
	if _, err := docker.EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{old}, "production", own); err != nil {
		t.Fatal(err)
	}
	s3 := func(args ...string) (string, error) {
		cmd := append([]string{"run", "--rm", "--network", docker.Current.Network(stack), "curlimages/curl:8.22.0", "-sS", "-f",
			"--aws-sigv4", "aws:amz:us-east-1:s3", "--user", "local:localsecret"}, args...)
		out, err := dockertest.CombinedOutput(cmd...)
		return string(out), err
	}
	var answer error
	if !poll.Until(t.Context(), 60*time.Second, 250*time.Millisecond, func() bool { _, answer = s3("http://s3:9000/"); return answer == nil }) {
		t.Fatalf("the old store did not answer: %v", answer)
	}
	for _, args := range [][]string{{"-X", "PUT", "http://s3:9000/app"}, {"-X", "PUT", "--data-binary", "kept object", "http://s3:9000/app/notes/one.txt"}} {
		if out, err := s3(args...); err != nil {
			t.Fatalf("the old store: %v %s", err, out)
		}
	}
	if _, err := docker.EnsureStandingServices(t.Context(), dk, stack, []deploy.ServiceSpec{current}, "production", own); err != nil {
		t.Fatalf("the old store was not remade on its volume: %v", err)
	}
	health := ""
	if !poll.Until(t.Context(), 60*time.Second, 250*time.Millisecond, func() bool {
		out, _ := dockertest.Output("inspect", "-f", "{{.State.Health.Status}}", docker.Current.Service(stack, "s3"))
		health = strings.TrimSpace(string(out))
		return health == "healthy"
	}) {
		t.Fatalf("the remade store did not mark itself ready: %s", health)
	}
	if out, err := s3("http://s3:9000/app/notes/one.txt"); err != nil || out != "kept object" {
		t.Fatalf("the object after the remake: %q %v", out, err)
	}
	if out, err := s3("-X", "PUT", "http://s3:9000/other"); err == nil || !strings.Contains(out, "403") {
		t.Fatalf("the app's key made a bucket after the remake: %v %s", err, out)
	}
}

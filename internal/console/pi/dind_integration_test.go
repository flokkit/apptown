//go:build integration

package pi

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/poll"
)

// A real private daemon: the client sees the same /work, builds and runs an
// image, reaches a published port, and reconnects after either side is lost.
func TestIntegrationConsoleDind(t *testing.T) {
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	}
	r := New(Config{Run: run, Image: func() string { return "apptown-agent" }, Dind: DindConfig{Image: "docker:27-dind"}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := func(args ...string) string {
		t.Helper()
		out, err := r.command(ctx, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	in := console.Launch{ID: fmt.Sprintf("test%x", time.Now().UnixNano()), Dir: t.TempDir(), Workspace: console.Workspace{Path: t.TempDir(), Writable: true}, AgentSession: "console", Docker: true}
	t.Cleanup(func() {
		cleanup, end := context.WithTimeout(context.Background(), 30*time.Second)
		defer end()
		_, _ = r.command(cleanup, "rm", "-f", ContainerName(in.ID))
		if err := r.disposeDindWithin(cleanup, in.ID, true); err != nil {
			t.Error(err)
		}
		r.Close()
	})
	for name, body := range map[string]string{
		"marker":     "console-workspace",
		"Dockerfile": "FROM busybox:1.37\nCOPY marker /site/index.html\nRUN test \"$(cat /site/index.html)\" = console-workspace\nCMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/site\"]\n",
	} {
		if err := os.WriteFile(filepath.Join(in.Workspace.Path, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ensureDind(in); err != nil {
		t.Fatal(err)
	}
	t.Log("private daemon ready")
	agentImage := os.Getenv("APPTOWN_TEST_AGENT_IMAGE")
	if agentImage == "" {
		agentImage = "apptown-agent"
	}
	args := []string{"run", "-d", "--name", ContainerName(in.ID), "--entrypoint", "sh", "-v", in.Workspace.Path + ":/work", "-v", in.Dir + ":/console",
		"--network", dindName(in.ID), "-e", "DOCKER_HOST=tcp://dind:2375", "-e", "DOCKER_CONFIG=/console/docker-client", "-e", "BUILDX_BUILDER=apptown",
		agentImage, "-c", "sleep infinity"}
	command(args...)
	client := func(args ...string) string {
		t.Helper()
		return command(append([]string{"exec", ContainerName(in.ID)}, args...)...)
	}
	if host, nested := command("info", "--format", "{{.ID}}"), client("docker", "info", "--format", "{{.ID}}"); host == nested {
		t.Fatal("console reached the host daemon")
	}
	client("docker", "buildx", "build", "--load", "-t", "console-smoke", "/work")
	client("docker", "build", "-t", "console-plain-build", "/work")
	client("docker", "image", "inspect", "console-plain-build")
	if out := client("docker", "run", "--rm", "-v", "/work:/work", "--entrypoint", "cat", "console-smoke", "/work/marker"); out != "console-workspace" {
		t.Fatalf("nested workspace mount: %s", out)
	}
	client("docker", "run", "-d", "--name", "game", "-p", "8080:8080", "console-smoke")
	if out := client("curl", "--fail", "--silent", "--retry", "5", "--retry-connrefused", "--retry-delay", "1", "http://dind:8080"); out != "console-workspace" {
		t.Fatalf("private published port: %s", out)
	}
	// A daemon stop/start retains images, and a restarted runtime adopts it.
	before := command("inspect", "--format", "{{.Id}}", dindName(in.ID))
	if err := r.disposeDindWithin(ctx, in.ID, false); err != nil {
		t.Fatal(err)
	}
	b := New(Config{Run: run, Image: func() string { return "apptown-agent" }, Dind: DindConfig{Image: "docker:27-dind"}})
	defer b.Close()
	if err := b.ensureDind(in); err != nil {
		t.Fatalf("%v; workspace=%q; sidecar=%s", err, in.Workspace.Path,
			command("inspect", "--format", `{{json .HostConfig.Binds}} {{json .Mounts}} {{json .Config.Labels}}`, dindName(in.ID)))
	}
	if after := command("inspect", "--format", "{{.Id}}", dindName(in.ID)); after != before {
		t.Fatal("resume replaced durable Docker state")
	}
	client("docker", "image", "inspect", "console-smoke")
	// A removed sidecar is recreated; the surviving client's endpoint remains valid.
	command("rm", "-f", "-v", dindName(in.ID))
	if err := r.ensureDind(in); err != nil {
		t.Fatal(err)
	}
	client("docker", "info")
	command("network", "disconnect", dindName(in.ID), ContainerName(in.ID))
	if err := r.connectClient(in.ID); err != nil {
		t.Fatal(err)
	}
	client("docker", "info")
	// A daemon killed before it could clean up (its stop's grace ran out)
	// comes back: its run state does not outlive the container. The pid
	// files name pid 1, which is always running in the restarted
	// container, so stale ones would always be taken for a live daemon.
	command("exec", dindName(in.ID), "sh", "-c", "echo 1 > /run/docker.pid && echo 1 > /run/docker/containerd/containerd.pid")
	command("kill", dindName(in.ID))
	if err := r.ensureDind(in); err != nil {
		t.Fatalf("a killed private daemon did not come back: %v", err)
	}
	client("docker", "info")
}

// With real Docker: a save cancelled
// while it writes does not leave its container running: cancelling removes
// it, and so would the chat's stop or removal; the runtime's status shows
// no script afterwards.
func TestIntegrationACancelledScriptDoesNotOutliveItsChat(t *testing.T) {
	id := fmt.Sprintf("scriptcancel%d", os.Getpid())
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	docker("image", "inspect", "busybox:latest")
	t.Cleanup(func() {
		for _, cid := range strings.Fields(docker("ps", "-aq", "--filter", "label=forge.owner="+id)) {
			docker("rm", "-f", cid)
		}
	})
	r := New(Config{Run: func(ctx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	}})
	defer r.Close()
	in := console.Launch{ID: id, Image: "busybox:latest", Workspace: console.Workspace{Path: t.TempDir(), Writable: true}}
	run := func(ctx context.Context) chan error {
		done := make(chan error, 1)
		go func() {
			done <- r.Script(ctx, in, "touch /work/started; while true; do echo writing >> /work/writer; sleep 0.1; done", nil, io.Discard)
		}()
		if !poll.Until(context.Background(), 20*time.Second, 20*time.Millisecond, func() bool {
			_, err := os.Stat(filepath.Join(in.Workspace.Path, "started"))
			return err == nil
		}) {
			t.Fatal("the script did not start")
		}
		return done
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := run(ctx)
	cancel()
	<-done
	if left := docker("ps", "-aq", "--filter", "label=forge.owner="+id); left != "" {
		t.Fatalf("a cancelled script's container is still there: %s", left)
	}
	// One left running by a client that went away is removed by a stop,
	// and shown by status until then.
	if err := os.Remove(filepath.Join(in.Workspace.Path, "started")); err != nil {
		t.Fatal(err)
	}
	docker("run", "-d", "--name", "forge-console-script-"+id, "--label", "forge.owner="+id, "-v", in.Workspace.Path+":/work", "busybox:latest", "sh", "-c", "while true; do sleep 1; done")
	if st, err := r.Status(context.Background(), id); err != nil || !st.ScriptExists {
		t.Fatalf("status with a script running: %+v %v", st, err)
	}
	if err := r.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if st, err := r.Status(context.Background(), id); err != nil || st.ScriptExists {
		t.Fatalf("status after the stop: %+v %v", st, err)
	}
	if left := docker("ps", "-aq", "--filter", "label=forge.owner="+id); left != "" {
		t.Fatalf("a stop left the script's container: %s", left)
	}
}

//go:build integration

package worker_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/worker"
)

// volumesOf names a container's volume mounts, anonymous and named.
func volumesOf(t *testing.T, container string) []string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", `{{range .Mounts}}{{if eq .Type "volume"}}{{println .Name}}{{end}}{{end}}`, container).Output()
	if err != nil {
		t.Fatalf("inspecting %s: %v", container, err)
	}
	return strings.Fields(string(out))
}

func volumeExists(name string) bool {
	return exec.Command("docker", "volume", "inspect", name).Run() == nil
}

// With real Docker: removing a preview takes every volume its containers
// had: the app's anonymous volume (its image's own VOLUME), its service's
// (redis declares one), and its named data volume.
func TestIntegrationRemovingAWorkerPreviewRemovesItsAnonymousVolumes(t *testing.T) {
	w := startPreviewWorker(t)
	// The app's image declares a volume of its own: an anonymous volume.
	contract := "version: 1\napp:\n  web:\n    build: .\n    ports: [\"80\"]\n    public: true\n    dataDir: /data\n"
	tree := siteTree(t, contract, map[string]string{"Dockerfile": "FROM caddy:2-alpine\nVOLUME /config\n"})
	c, err := docker.ReadSelectedContract(tree)
	if err != nil {
		t.Fatal(err)
	}
	prepared := docker.Prepared{Version: docker.PreparedVersion, Contract: c, BrowserEnv: "{}",
		Services: []deploy.ServiceSpec{{Name: "cache", Image: "redis:7-alpine"}}}
	st, err := w.previews.Boot(context.Background(), worker.PreviewSpec{Name: w.name, StackID: w.stack, Slug: "site",
		Incarnation: "volumes-a", ProbeSeconds: 60}, prepared, tree)
	if err != nil || !st.Running || st.Error != "" {
		t.Fatalf("boot: %+v %v", st, err)
	}
	service := docker.Current.Service(w.stack, "cache")
	volumes := append(volumesOf(t, w.name), volumesOf(t, service)...)
	t.Cleanup(func() {
		for _, v := range volumes {
			_ = exec.Command("docker", "volume", "rm", "-f", v).Run()
		}
	})
	named := docker.Current.DataVolume(w.name)
	anonymous := 0
	for _, v := range volumes {
		if v != named {
			anonymous++
		}
	}
	if anonymous < 2 {
		t.Fatalf("the fixture has no anonymous volumes on both the app and the service: %q", volumes)
	}
	if err := w.previews.Remove(context.Background(), w.name); err != nil {
		t.Fatal(err)
	}
	for _, v := range volumes {
		if volumeExists(v) {
			t.Errorf("removing the preview left volume %s", v)
		}
	}
}

// With real Docker: a preview replaced by another incarnation starts with
// fresh data; nothing the earlier one wrote carries over.
func TestIntegrationAReplacedWorkerPreviewStartsWithFreshData(t *testing.T) {
	w := startPreviewWorker(t)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", docker.Current.DataVolume(w.name)).Run() })
	contract := "version: 1\napp:\n  web:\n    image: caddy:2-alpine\n    ports: [\"80\"]\n    public: true\n    dataDir: /data\n"
	first := w.boot(t, "fresh-a", siteTree(t, contract, map[string]string{}))
	if !first.Running || first.Error != "" {
		t.Fatalf("boot: %+v", first)
	}
	if out, err := exec.Command("docker", "exec", w.name, "sh", "-c", "echo earlier > /data/marker").CombinedOutput(); err != nil {
		t.Fatalf("writing the earlier incarnation's data: %v %s", err, out)
	}
	next := w.boot(t, "fresh-b", siteTree(t, contract, map[string]string{}))
	if !next.Running || next.ContainerID == first.ContainerID {
		t.Fatalf("the replacement: %+v", next)
	}
	if out, err := exec.Command("docker", "exec", w.name, "cat", "/data/marker").CombinedOutput(); err == nil {
		t.Fatalf("the replacement found the earlier incarnation's data: %s", out)
	}
}

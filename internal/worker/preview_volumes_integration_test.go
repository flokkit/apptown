//go:build integration

package worker_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
)

func TestIntegrationRemovingAWorkerPreviewRemovesItsVolumes(t *testing.T) {
	w := startPreviewWorker(t)
	volume := docker.DataVolumeName(w.name)
	t.Cleanup(func() {
		_ = w.previews.Remove(context.Background(), w.name)
		_ = exec.Command("docker", "volume", "rm", "-f", volume).Run()
	})
	contract := "version: 1\napp:\n  web:\n    image: caddy:2-alpine\n    ports: [\"80\"]\n    public: true\n    dataDir: /data\n"
	st := w.boot(t, "review-volume", siteTree(t, contract, map[string]string{}))
	if !st.Running || st.Error != "" {
		t.Fatalf("boot: %+v", st)
	}
	mounts, err := exec.Command("docker", "inspect", "-f", `{{range .Mounts}}{{if eq .Type "volume"}}{{println .Name}}{{end}}{{end}}`, w.name).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = w.previews.Remove(context.Background(), w.name)
		for _, name := range strings.Fields(string(mounts)) {
			_ = exec.Command("docker", "volume", "rm", "-f", name).Run()
		}
	})
	if out, err := exec.Command("docker", "volume", "inspect", volume).CombinedOutput(); err != nil {
		t.Fatalf("volume was never created: %v %s", err, out)
	}
	if err := w.previews.Remove(context.Background(), w.name); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("docker", "volume", "inspect", volume).Run(); err == nil {
		t.Fatalf("DELETE answered success but left reusable preview data volume %s", volume)
	}
}

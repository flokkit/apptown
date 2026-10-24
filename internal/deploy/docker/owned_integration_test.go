//go:build integration

package docker

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/poll"
	"github.com/flokkit/apptown/stock"
)

// taggedImage is what the inventory test runs: an image with a tag. The
// pinned busybox, pulled by digest alone, is the inventory's other case.
const taggedImage = "caddy:2-alpine"

// On a real daemon: a standing deployment's services record their
// containers, the network the boot made and each need's named data volume;
// take-down without a final backup deletes the containers and the network
// and keeps the data volume, with the need's file still in it; with one it
// deletes the volume too. A network that already existed, and a volume the
// list does not name, are never touched.
func TestIntegrationTakeDownDeletesExactlyWhatTheBootRecorded(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	stack := fmt.Sprintf("env-itown%d-main", time.Now().UnixNano()%1_000_000)
	foreign := "itown-foreign-" + stack
	cleanup := func() {
		for _, c := range []string{Current.Service(stack, "store"), Current.Service(stack, "worker")} {
			_ = exec.Command("docker", "rm", "-f", "-v", c).Run()
		}
		_ = exec.Command("docker", "network", "rm", Current.Network(stack)).Run()
		_ = exec.Command("docker", "volume", "rm", Current.NeedVolume(stack, "store"), foreign).Run()
	}
	cleanup()
	t.Cleanup(cleanup)
	if out, err := exec.Command("docker", "volume", "create", foreign).CombinedOutput(); err != nil {
		t.Fatalf("a volume not on the list: %v %s", err, out)
	}

	var owned []deploy.Owned
	own := Ownership{Record: func(o deploy.Owned) error { owned = append(owned, o); return nil }, Needs: map[string]string{"store": "/data"}}
	svcs := []deploy.ServiceSpec{
		{Name: "store", Image: taggedImage, Command: []string{"sh", "-c", "echo kept > /data/f && sleep 3600"}},
		{Name: "worker", Image: taggedImage, Command: []string{"sleep", "3600"}},
	}
	if _, err := EnsureStandingServices(context.Background(), dk, stack, svcs, "synthetic", own); err != nil {
		t.Fatal(err)
	}
	// Inventory uses the real Docker list formats, including empty labels,
	// need volumes and networks, and Docker's real answer about recorded
	// images: a tagged one, one pulled by digest alone (no listing shows it),
	// and one the host lacks.
	if out, err := dk.Run(t.Context(), "pull", stock.Images().DF); err != nil {
		t.Fatalf("pulling %s: %v %s", stock.Images().DF, err, out)
	}
	gone := Current.BuildTag("itest-gone-" + stack)
	recordedImages := func(context.Context) ([]string, error) { return []string{taggedImage, stock.Images().DF, gone}, nil }
	resources, err := (Inventory{Scope: func(context.Context) (Scope, error) { return dk, nil }, Images: recordedImages}).Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resources {
		if r.Kind == "image" && r.Name == gone {
			t.Errorf("an image the host lacks read as present: %+v", r)
		}
	}
	for _, want := range []struct{ kind, name string }{
		{"container", Current.Service(stack, "store")}, {"container", Current.Service(stack, "worker")},
		{"network", Current.Network(stack)}, {"volume", Current.NeedVolume(stack, "store")}, {"image", taggedImage}, {"image", stock.Images().DF},
	} {
		found := false
		for _, r := range resources {
			if r.Kind == want.kind && r.Name == want.name {
				found = true
			}
		}
		if !found {
			t.Errorf("real inventory did not confirm %s %s", want.kind, want.name)
		}
	}
	var kinds []string
	for _, o := range owned {
		kinds = append(kinds, o.Kind+":"+o.Need)
		if o.Kind == "container" && len(o.ID) != 64 {
			t.Errorf("a container recorded without its ID: %+v", o)
		}
	}
	if want := []string{"network:", "volume:store", "container:store", "container:"}; !slices.Equal(kinds, want) {
		t.Fatalf("recorded %v want %v (%+v)", kinds, want, owned)
	}
	// The need wrote into its named volume.
	var data []byte
	if !poll.Until(context.Background(), 20*time.Second, 100*time.Millisecond, func() bool {
		data, _ = exec.Command("docker", "exec", Current.Service(stack, "store"), "cat", "/data/f").CombinedOutput()
		return strings.TrimSpace(string(data)) == "kept"
	}) {
		t.Fatalf("the need never wrote its data: %s", data)
	}

	out := TearDownOwned(context.Background(), dk, owned, "", false)
	if dk.OK(context.Background(), "inspect", Current.Service(stack, "store")) || dk.OK(context.Background(), "inspect", Current.Service(stack, "worker")) || dk.OK(context.Background(), "network", "inspect", Current.Network(stack)) {
		t.Fatalf("containers or network left after take-down: %+v", out)
	}
	if !dk.OK(context.Background(), "volume", "inspect", Current.NeedVolume(stack, "store")) || len(out.Left) != 1 || !out.Left[0].Data {
		t.Fatalf("the need's data was not kept without a backup: %+v", out)
	}
	check, err := exec.Command("docker", "run", "--rm", "-v", Current.NeedVolume(stack, "store")+":/data", stock.Images().DF, "cat", "/data/f").CombinedOutput()
	if err != nil || strings.TrimSpace(string(check)) != "kept" {
		t.Fatalf("the kept volume lost its data: %v %s", err, check)
	}

	out = TearDownOwned(context.Background(), dk, owned, "", true)
	if dk.OK(context.Background(), "volume", "inspect", Current.NeedVolume(stack, "store")) || len(out.Left) != 0 {
		t.Fatalf("the backed-up volume stayed: %+v", out)
	}
	if !dk.OK(context.Background(), "volume", "inspect", foreign) {
		t.Fatal("a volume not on the list was deleted")
	}

	// A network that existed before the boot is used, never recorded.
	if out, err := exec.Command("docker", "network", "create", Current.Network(stack)).CombinedOutput(); err != nil {
		t.Fatalf("network: %v %s", err, out)
	}
	owned = nil
	if _, err := EnsureStandingServices(context.Background(), dk, stack, svcs[1:], "synthetic", own); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(owned, func(o deploy.Owned) bool { return o.Kind == "network" }) {
		t.Fatalf("a network the boot did not make was recorded: %+v", owned)
	}
	TearDownOwned(context.Background(), dk, owned, "", true)
	if !dk.OK(context.Background(), "network", "inspect", Current.Network(stack)) {
		t.Fatal("a network AppTown did not make was deleted")
	}
}

// On a real daemon, a container on the list goes without its
// anonymous volumes, even after a final backup of other data. A volume the
// list does not name is never deleted, however it came to be.
func TestIntegrationTakeDownKeepsAContainersUnrecordedAnonymousVolume(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	name := fmt.Sprintf("itown-anon-%d", time.Now().UnixNano()%1_000_000)
	id := runDetached(t, "run", "-d", "--name", name, "-v", "/unrecorded", stock.Images().DF, "sleep", "3600")
	vol := strings.TrimSpace(dk.Text(context.Background(), "inspect", "-f", `{{range .Mounts}}{{.Name}}{{end}}`, id))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", id).Run()
		_ = exec.Command("docker", "volume", "rm", vol).Run()
	})
	if vol == "" {
		t.Fatal("no anonymous volume")
	}
	taken := TearDownOwned(context.Background(), dk, []deploy.Owned{{Kind: "container", ID: id, Name: name}}, "", true)
	if dk.OK(context.Background(), "inspect", id) || len(taken.Deleted) != 1 {
		t.Fatalf("the listed container: %+v", taken)
	}
	if !dk.OK(context.Background(), "volume", "inspect", vol) {
		t.Fatal("an anonymous volume the list does not name was deleted with its container")
	}
}

// On a real daemon: a standing need's container keeps a rotated log. It
// writes 45 MiB; Docker keeps at most three 10 MiB files of it, so what
// docker logs still holds is at most 30 MiB, and the recent log reads its
// last line. (Rotation discards the oldest output; that is the bound.)
func TestIntegrationAStandingContainersLogIsRotated(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	stack := fmt.Sprintf("env-ilogs%d-main", time.Now().UnixNano()%1_000_000)
	name := Current.Service(stack, "chatty")
	cleanup := func() {
		_ = exec.Command("docker", "rm", "-f", "-v", name).Run()
		_ = exec.Command("docker", "network", "rm", Current.Network(stack)).Run()
	}
	cleanup()
	t.Cleanup(cleanup)
	svcs := []deploy.ServiceSpec{{Name: "chatty", Image: stock.Images().DF, Command: []string{"sh", "-c",
		"head -c 47185920 /dev/zero | tr '\\0' x | fold -w 65536; echo; echo done; sleep 3600"}}}
	if _, err := EnsureStandingServices(context.Background(), dk, stack, svcs, "synthetic", Ownership{}); err != nil {
		t.Fatal(err)
	}
	config, err := exec.Command("docker", "inspect", "--format", "{{json .HostConfig.LogConfig}}", name).CombinedOutput()
	if err != nil || !strings.Contains(string(config), `"json-file"`) || !strings.Contains(string(config), `"max-size":"10m"`) || !strings.Contains(string(config), `"max-file":"3"`) {
		t.Fatalf("the container's log configuration: %s %v", config, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var recent RecentLog
	for {
		recent, err = ReadRecentLog(ctx, dk, name)
		if err == nil && strings.Contains(recent.Text, "done") {
			break
		}
		if err := poll.Wait(ctx, 500*time.Millisecond); err != nil {
			t.Fatalf("the last line was never read: %v (%+v)", err, recent)
		}
	}
	all, err := exec.Command("docker", "logs", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 || len(all) > 30<<20 {
		t.Errorf("docker logs holds %d bytes of 45 MiB written; the bound is 30 MiB", len(all))
	}
}

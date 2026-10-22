//go:build integration

package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// On a real daemon: a deployment made before its names were by key, whose
// service keeps its data on an anonymous volume and whose app keeps its
// own in its old data volume, is moved. The copies hold the same files
// with the same owners and modes (the folder churned first, so its own
// size differs); the old containers and network are gone; the old volumes
// are still there, data intact; and the old labels are read where the
// switch needs them.
func TestIntegrationAMoveCopiesRealDataAndKeepsTheOld(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	slug := fmt.Sprintf("itmove%d", time.Now().UnixNano()%1_000_000)
	from, to := BeforeKey(Before1, slug, "main"), ByKey(keyed(byte(time.Now().UnixNano()%250+1)))
	image := check.ShImage
	// The service and the app run busybox: an image declaring no volume of
	// its own, so nothing anonymous is left behind but the data volume.
	const box = "busybox:1.37"
	var anon string
	cleanup := func() {
		_ = exec.Command("docker", "rm", "-f", from.App(), from.Service("store")).Run()
		_ = exec.Command("docker", "network", "rm", from.Network()).Run()
		_ = exec.Command("docker", "volume", "rm", "-f", from.DataVolume(), to.DataVolume(), to.NeedVolume("store"), anon).Run()
	}
	t.Cleanup(cleanup)
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("network", "create", from.Network())
	run("volume", "create", "--label", dockerlabel.Before1(dockerlabel.Owner)+"="+from.App(), from.DataVolume())
	// The service: data written as uid 70 into a folder that once held many
	// files, on an anonymous volume.
	run("run", "-d", "--init", "--name", from.Service("store"), "--network", from.Network(), "--label", dockerlabel.Before1(dockerlabel.Class)+"=env",
		"--label", dockerlabel.Before1(dockerlabel.Owner)+"="+from.Stack(), "-v", "/data", "--entrypoint", "sleep", box, "600")
	run("exec", from.Service("store"), "sh", "-c",
		`i=0; while [ "$i" -lt 500 ]; do touch /data/f$i; i=$((i+1)); done; rm /data/f*; echo rows > /data/table; mkdir /data/sub; echo x > /data/sub/y; ln -s table /data/link; chown -R 70:70 /data; chmod 700 /data`)
	anon = run("inspect", "-f", `{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}`, from.Service("store"))
	run("run", "-d", "--init", "--name", from.App(), "--network", from.Network(), "-v", from.DataVolume()+":/app", "--entrypoint", "sleep", box, "600")
	run("exec", from.App(), "sh", "-c", "echo files > /app/f")

	var switched []deploy.Owned
	req := &BootRequest{Docker: dk, Name: to.App(), Names: to.Prefix(), StackID: to.Stack(), Persistent: true, Log: t.Logf,
		move: &bootMove{from: from, Move: Move{Switch: func(_ context.Context, made []deploy.Owned) error { switched = made; return nil }}}}
	req.own = Ownership{Needs: map[string]string{"store": "/data"}, Names: to.Prefix(), Persistent: true}
	if err := NewHost(Dependencies{ChownImage: image}).moveStack(context.Background(), req, "/app"); err != nil {
		t.Fatal(err)
	}
	if len(switched) != 2 {
		t.Fatalf("switched with %v", switched)
	}
	for _, gone := range []string{from.App(), from.Service("store")} {
		if there, _ := dk.Exists(context.Background(), "inspect", gone); there {
			t.Errorf("%s is still there", gone)
		}
	}
	if there, _ := dk.Exists(context.Background(), "network", "inspect", from.Network()); there {
		t.Error("the old network is still there")
	}
	got := run("run", "--rm", "-v", to.NeedVolume("store")+":/d:ro", "--entrypoint", "sh", image, "-c", "cat /d/table; stat -c '%a %u' /d; readlink /d/link")
	if got != "rows\n700 70\ntable" {
		t.Fatalf("the copied need holds %q", got)
	}
	if got := run("run", "--rm", "-v", to.DataVolume()+":/d:ro", "--entrypoint", "cat", image, "/d/f"); got != "files" {
		t.Fatalf("the copied app data holds %q", got)
	}
	if got := run("run", "--rm", "-v", anon+":/d:ro", "--entrypoint", "cat", image, "/d/table"); got != "rows" {
		t.Fatalf("the old volume lost its data: %q", got)
	}
}

// The tree check passes a correct copy of a folder that once held many
// files and fails a copy with one changed byte, owner or link target.
func TestIntegrationTheCopyCheckSeesWhatDiffers(t *testing.T) {
	setup := `mkdir /from /to
i=0; while [ "$i" -lt 1000 ]; do touch /from/f$i; i=$((i+1)); done; rm /from/f*
echo data > /from/kept; mkdir /from/sub; echo x > /from/sub/y; ln -s kept /from/link
chown -R 70:70 /from; chmod 700 /from
cp -a /from/. /to/; chown 70:70 /to; chmod 700 /to
`
	for name, change := range map[string]string{"nothing": "", "a byte": "echo dat0 > /to/kept\n", "an owner": "chown 0:0 /to/sub/y\n", "a link": "ln -sfn sub /to/link\n"} {
		out, err := exec.Command("docker", "run", "--rm", "--network", "none", "--entrypoint", "sh", check.ShImage, "-c", setup+change+sameTree).CombinedOutput()
		if (change == "") != (err == nil) {
			t.Errorf("%s changed: %v: %s", name, err, out)
		}
	}
}

// A file whose name holds a line break cannot be listed one per line, so
// the check refuses the copy instead of counting that file as compared
// (review RR-C01: a corrupted copy of one was accepted).
func TestIntegrationTheCopyCheckRefusesWhatItCannotList(t *testing.T) {
	setup := `set -e
mkdir /from /to
f=$(printf 'upload\n2026.txt')
printf original > "/from/$f"
cp -a /from/. /to/
printf corruptx > "/to/$f"
`
	out, err := exec.Command("docker", "run", "--rm", "--network", "none", "--entrypoint", "sh", check.ShImage, "-c", setup+sameTree).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "line break") {
		t.Fatalf("a copy holding a name it cannot list was accepted: %v %s", err, out)
	}
}

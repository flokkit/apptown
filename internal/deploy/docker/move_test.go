package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// moveWorld is a daemon holding one deployment made before its names were
// by key (shop/main, forge-): its app, its Postgres service keeping data on
// an anonymous volume, and the service's container found by its old owner
// label. copyFails makes the copy's check fail.
type moveWorld struct {
	client    *fakeClient
	from, to  Names
	mount     string // what the service mounts at its data path: "volume\t<name>" or "bind\t<path>"
	copyFails bool
	switched  [][]deploy.Owned
	logs      []string
}

const moveData = "/var/lib/postgresql/data"

func newMoveWorld() *moveWorld {
	w := &moveWorld{from: BeforeKey(Before1, "shop", "main"), to: ByKey(keyed(9)), mount: "volume\t0eb9e689anonymous"}
	w.client = &fakeClient{reply: func(req Command) ([]byte, error) {
		a := req.Args
		line := strings.Join(a, " ")
		switch {
		case a[0] == "inspect" && a[1] == "-f" && strings.Contains(a[2], ".Mounts"):
			kind, src, _ := strings.Cut(w.mount, "\t")
			if kind == "volume" {
				return []byte("volume\t" + src + "\t/x\t" + moveData + "\n"), nil
			}
			return []byte("bind\t\t" + src + "\t" + moveData + "\n"), nil
		case a[0] == "inspect" && (a[1] == w.from.Service("postgres") || a[1] == w.from.App()):
			return []byte("[{}]"), nil
		case a[0] == "volume" && a[1] == "inspect":
			return []byte("Error response from daemon: get " + a[2] + ": no such volume"), errors.New("exit status 1")
		case a[0] == "ps" && strings.Contains(line, "label="+dockerlabel.Before1(dockerlabel.Owner)+"="+w.from.Stack()):
			return []byte(w.from.Service("postgres") + "\n"), nil
		case a[0] == "ps":
			return nil, nil
		case a[0] == "run" && w.copyFails:
			return []byte("the copy differs from the original"), errors.New("exit status 3")
		case a[0] == "run":
			return []byte("12 entries, 40 KB"), nil
		}
		return nil, nil
	}}
	return w
}

func (w *moveWorld) move(t *testing.T) error {
	t.Helper()
	req := &BootRequest{Docker: Scope{Client: w.client}, Name: w.to.App(), Names: w.to.Prefix(), StackID: w.to.Stack(), Persistent: true,
		Log: func(f string, args ...any) { w.logs = append(w.logs, fmt.Sprintf(f, args...)) },
		move: &bootMove{from: w.from, Move: Move{Switch: func(_ context.Context, made []deploy.Owned) error {
			w.switched = append(w.switched, made)
			return nil
		}}}}
	req.own = Ownership{Needs: map[string]string{"postgres": moveData}, Names: w.to.Prefix(), Persistent: true}
	return NewHost(Dependencies{ChownImage: "helper:1"}).moveStack(context.Background(), req, "")
}

func (w *moveWorld) ran(prefix string) int {
	return slices.IndexFunc(w.client.args, func(a []string) bool { return strings.HasPrefix(strings.Join(a, " "), prefix) })
}

// The move stops the old app, then its stack; copies the volume the
// service keeps its data in (an anonymous one here) into the new name's
// need volume, checked; switches the record with exactly that volume; then
// removes the old containers and network and keeps every old volume, saying
// so.
//
// Kills: a move that copies while the old stack runs, that copies from the
// old named volume instead of what the service mounts, that switches before
// copying, or that deletes the old data.
func TestAMoveCopiesTheDataWhereItIsThenSwitchesAndKeepsTheOld(t *testing.T) {
	t.Parallel()
	w := newMoveWorld()
	if err := w.move(t); err != nil {
		t.Fatal(err)
	}
	stopApp, stopSvc := w.ran("stop -t "+moveStopWait+" "+w.from.App()), w.ran("stop -t "+moveStopWait+" "+w.from.Service("postgres"))
	copied := w.ran("run --rm --network none --entrypoint sh -v 0eb9e689anonymous:/from:ro -v " + w.to.NeedVolume("postgres") + ":/to helper:1 -c")
	removed := w.ran("rm -f " + w.from.App())
	if stopApp < 0 || stopSvc < stopApp || copied < stopSvc || removed < copied {
		t.Fatalf("stop app %d, stop service %d, copy %d, remove %d:\n%v", stopApp, stopSvc, copied, removed, w.client.args)
	}
	want := []deploy.Owned{{Kind: "volume", ID: w.to.NeedVolume("postgres"), Data: true, Need: "postgres"}}
	if len(w.switched) != 1 || !slices.Equal(w.switched[0], want) {
		t.Fatalf("switched with %v, want %v", w.switched, want)
	}
	if w.ran("rm -f "+w.from.Service("postgres")) < 0 || w.ran("network rm "+w.from.Network()) < 0 {
		t.Fatalf("the old service or network was left: %v", w.client.args)
	}
	if w.ran("volume rm 0eb9e689anonymous") >= 0 || w.ran("rm -f -v") >= 0 {
		t.Fatalf("old data was deleted: %v", w.client.args)
	}
	if !slices.ContainsFunc(w.logs, func(l string) bool {
		return strings.Contains(l, "Kept on the host") && strings.Contains(l, "0eb9e689anonymous")
	}) {
		t.Fatalf("the kept volume is not named: %v", w.logs)
	}
}

// A copy that does not check out puts the old stack back (services, then
// the app), removes the copies, switches nothing, and is a known outcome.
//
// Kills: a failed copy that switches the record, or leaves the app down.
func TestAFailedCopyPutsTheOldStackBack(t *testing.T) {
	t.Parallel()
	w := newMoveWorld()
	w.copyFails = true
	err := w.move(t)
	if err == nil || !AsBootError(err).KnownOutcome() || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("a failed copy: %v", err)
	}
	if len(w.switched) != 0 {
		t.Fatalf("the record was switched: %v", w.switched)
	}
	startSvc, startApp := w.ran("start "+w.from.Service("postgres")), w.ran("start "+w.from.App())
	if startSvc < 0 || startApp < startSvc || w.ran("volume rm "+w.to.NeedVolume("postgres")) < 0 {
		t.Fatalf("the old stack was not put back, or the copy kept: %v", w.client.args)
	}
	if w.ran("rm -f") >= 0 {
		t.Fatalf("an old container was removed: %v", w.client.args)
	}
}

// Data kept in a host folder is not moved: refused before anything stops.
//
// Kills: a move that stops the app before finding where its data is.
func TestAMoveOfDataInAHostFolderIsRefusedBeforeAnythingStops(t *testing.T) {
	t.Parallel()
	w := newMoveWorld()
	w.mount = "bind\t/srv/pg"
	err := w.move(t)
	if err == nil || !AsBootError(err).KnownOutcome() || !strings.Contains(err.Error(), "move it by hand") {
		t.Fatalf("a bind-mounted need: %v", err)
	}
	if w.ran("stop") >= 0 || len(w.switched) != 0 {
		t.Fatalf("something stopped or switched: %v", w.client.args)
	}
}

// The driver boots a move under the names by key, with the deployment's
// UUID as its identity from the first checkpoint, and refuses to move a
// deployment already named by key.
//
// Kills: a move booted under the old names, or with the old label.
func TestTheDriverBootsAMoveUnderItsNamesByKey(t *testing.T) {
	t.Parallel()
	body := []byte(`{"version":1,"revision":"abc"}`)
	for _, prefix := range []string{"forge-", ""} {
		boot := &fakeBoot{daemon: &fakeDaemon{}}
		d := &Driver{Scope: func(context.Context) (Scope, error) { return Scope{Client: boot.daemon}, nil }, Boot: boot,
			URL: func(_, _ string, port int) string { return "" }, AfterRelease: func(deploy.Placement) {}, Move: &Move{Switch: func(context.Context, []deploy.Owned) error { return nil }}}
		c := mustContract(t)
		art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)), Source: &deploy.Source{Workspace: t.TempDir(), Revision: "abc", Slug: "shop", Contract: c}}
		id := keyed(9)
		var first map[string]any
		_, err := d.Reconcile(context.Background(), deploy.Placement{HostPrefix: prefix, ID: id, Label: "41", Name: "main"}, 1, art, "k", func(string, ...any) {},
			func(v map[string]any) error {
				if first == nil {
					first = v
				}
				return nil
			})
		if prefix == "" {
			if err == nil || !strings.Contains(err.Error(), "already named by its key") || boot.req.Name != "" {
				t.Fatalf("a key-named deployment was moved: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if boot.req.Name != ByKey(id).App() || boot.req.move == nil || boot.req.move.from != BeforeKey(Before1, "shop", "main") ||
			string(boot.req.Identity.Deployment) != id.String() || first["container"] != ByKey(id).App() {
			t.Fatalf("the move booted as %q from %+v with identity %q, first checkpoint %v", boot.req.Name, boot.req.move, boot.req.Identity.Deployment, first)
		}
	}
}

func mustContract(t *testing.T) *appcontract.Contract {
	t.Helper()
	c, err := appcontract.Parse([]byte("app:\n  game:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

package docker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/dockerlabel"
)

// ownedHost models the objects a Docker host holds, by kind and name, so a
// test can say what existed before a boot and see what a teardown removed.
type ownedHost struct {
	objects map[string]bool // "container c1", "network n", "volume v", "image i"
	mounts  map[string]string
	inUse   map[string]bool // images a container runs
	refuse  map[string]bool // objects whose removal fails
	unknown map[string]bool // objects the daemon cannot be asked about
	removed []string
	created []string
	runs    [][]string
	next    int
}

func newOwnedHost(existing ...string) *ownedHost {
	h := &ownedHost{objects: map[string]bool{}, mounts: map[string]string{}, inUse: map[string]bool{}, refuse: map[string]bool{}, unknown: map[string]bool{}}
	for _, o := range existing {
		h.objects[o] = true
	}
	return h
}

func (h *ownedHost) scope() Scope { return Scope{Client: &fakeClient{reply: h.reply}} }

func (h *ownedHost) reply(req Command) ([]byte, error) {
	a := req.Args
	gone := func(kind, name string) ([]byte, error) {
		return []byte("Error response from daemon: No such " + kind + ": " + name), errors.New("exit status 1")
	}
	remove := func(kind, name string) ([]byte, error) {
		if !h.objects[kind+" "+name] {
			return gone(kind, name)
		}
		if h.refuse[kind+" "+name] {
			return []byte("Error response from daemon: refused"), errors.New("exit status 1")
		}
		delete(h.objects, kind+" "+name)
		h.removed = append(h.removed, kind+" "+name)
		return nil, nil
	}
	switch {
	case len(a) == 3 && a[1] == "inspect" && (a[0] == "network" || a[0] == "volume" || a[0] == "image"):
		if h.unknown[a[0]+" "+a[2]] {
			return []byte("error during connect: connection reset by peer"), errors.New("exit status 1")
		}
		if !h.objects[a[0]+" "+a[2]] {
			return gone(a[0], a[2])
		}
		return nil, nil
	case len(a) >= 3 && (a[0] == "network" || a[0] == "volume") && a[1] == "create":
		h.objects[a[0]+" "+a[len(a)-1]] = true
		h.created = append(h.created, strings.Join(a, " "))
		return []byte(a[len(a)-1] + "\n"), nil
	case a[0] == "run" && slices.Contains(a, "-d"):
		h.runs = append(h.runs, a)
		h.next++
		name := a[slices.Index(a, "--name")+1]
		id := strings.Repeat(fmt.Sprint(h.next), 64)[:64]
		h.objects["container "+id] = true
		h.objects["container "+name] = true
		return []byte("Pulling...\n" + id + "\n"), nil
	case a[0] == "inspect" && len(a) == 4 && strings.Contains(a[2], ".Mounts"):
		if m := h.mounts[a[3]]; m != "" {
			return []byte(m + "\n"), nil
		}
		return nil, nil
	case a[0] == "inspect" && len(a) == 2:
		if !h.objects["container "+a[1]] {
			return gone("container", a[1])
		}
		return nil, nil
	case a[0] == "rm":
		return remove("container", a[len(a)-1])
	case a[0] == "network" && a[1] == "rm":
		return remove("network", a[2])
	case a[0] == "volume" && a[1] == "rm":
		return remove("volume", a[2])
	case a[0] == "rmi":
		if h.inUse[a[1]] {
			return []byte("Error response from daemon: conflict: unable to remove repository reference (must force) - container abc is using its referenced image"), errors.New("exit status 1")
		}
		return remove("image", a[1])
	}
	return nil, nil
}

type ownedList []deploy.Owned

func (l *ownedList) record(o deploy.Owned) error { *l = append(*l, o); return nil }

// A standing service is recorded with its container's ID; a need keeps
// its data in a named volume of the deployment's, made and recorded before
// its container mounts it. A network or volume is recorded only when this
// boot made it; one that already existed is used and never recorded.
func TestStandingServicesRecordWhatTheyCreate(t *testing.T) {
	t.Parallel()
	for _, existed := range []bool{false, true} {
		t.Run(fmt.Sprintf("existed %v", existed), func(t *testing.T) {
			t.Parallel()
			h := newOwnedHost()
			if existed {
				h.objects["network apptown-net-env-shop-main"] = true
				h.objects["volume apptown-need-env-shop-main-postgres"] = true
			}
			var got ownedList
			own := Ownership{Record: got.record, Needs: map[string]string{"postgres": "/var/lib/postgresql/data", "mailpit": ""}}
			svcs := []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16"}, {Name: "mailpit", Image: "mailpit"}, {Name: "worker", Image: "app"}}
			if _, err := EnsureStandingServices(context.Background(), h.scope(), "env-shop-main", svcs, "synthetic", own); err != nil {
				t.Fatal(err)
			}
			want := ownedList{
				{Kind: "container", ID: strings.Repeat("1", 64), Name: "apptown-svc-env-shop-main-postgres", Need: "postgres"},
				{Kind: "container", ID: strings.Repeat("2", 64), Name: "apptown-svc-env-shop-main-mailpit", Need: "mailpit"},
				{Kind: "container", ID: strings.Repeat("3", 64), Name: "apptown-svc-env-shop-main-worker"},
			}
			if !existed {
				want = append(ownedList{
					{Kind: "network", ID: "apptown-net-env-shop-main"},
					{Kind: "volume", ID: "apptown-need-env-shop-main-postgres", Data: true, Need: "postgres"},
				}, want...)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("recorded\n%+v\nwant\n%+v", got, want)
			}
			if !strings.Contains(strings.Join(h.runs[0], " "), "-v apptown-need-env-shop-main-postgres:/var/lib/postgresql/data") {
				t.Fatalf("postgres does not keep its data in the named volume: %v", h.runs[0])
			}
			if !existed && !strings.Contains(strings.Join(h.created, "\n"), "volume create --label apptown.owner=env-shop-main apptown-need-env-shop-main-postgres") {
				t.Fatalf("the need's volume is not labelled with its stack: %v", h.created)
			}
		})
	}
}

// The app's data volume is created before its container mounts it and
// recorded only when this boot created it.
func TestTheAppDataVolumeIsRecordedOnlyWhenTheBootMadeIt(t *testing.T) {
	t.Parallel()
	h := newOwnedHost("volume apptown-data-apptown-env-shop-old")
	var got ownedList
	own := Ownership{Record: got.record}
	for _, v := range []string{"apptown-data-apptown-env-shop-old", "apptown-data-apptown-env-shop-main"} {
		if err := ensureVolume(context.Background(), h.scope(), v, "x", "", own); err != nil {
			t.Fatal(err)
		}
	}
	if want := (ownedList{{Kind: "volume", ID: "apptown-data-apptown-env-shop-main", Data: true}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded %+v", got)
	}
}

// Take-down deletes exactly the list: containers, networks, volumes, then
// images no container uses. Data goes only with a final backup; a failed
// delete and kept data are reported, and what is already gone, or not on
// the list, is passed over.
func TestTearDownDeletesExactlyTheList(t *testing.T) {
	t.Parallel()
	list := []deploy.Owned{
		{Kind: "image", ID: "reg/shop@sha256:aa"},
		{Kind: "image", ID: "reg/shop@sha256:bb"},
		{Kind: "volume", ID: "anon-pg", Data: true, Need: "postgres"},
		{Kind: "volume", ID: "unreachable", Data: true},
		{Kind: "network", ID: "apptown-net-env-shop-main"},
		{Kind: "container", ID: "serving"},
		{Kind: "container", ID: "pg", Name: "apptown-svc-env-shop-main-postgres", Need: "postgres"},
		{Kind: "container", ID: "replaced-long-ago"},
		{Kind: "bucket", ID: "b"},
	}
	for _, backedUp := range []bool{true, false} {
		t.Run(fmt.Sprintf("backed up %v", backedUp), func(t *testing.T) {
			t.Parallel()
			h := newOwnedHost("container pg", "network apptown-net-env-shop-main", "volume anon-pg", "volume unreachable", "image reg/shop@sha256:aa", "image reg/shop@sha256:bb", "container someone-elses", "volume not-listed")
			h.unknown["volume unreachable"] = true // unknown is not gone
			h.inUse["reg/shop@sha256:bb"] = true
			h.refuse["network apptown-net-env-shop-main"] = true
			out := tearDownOwned(context.Background(), h.scope(), list, "serving", backedUp, nil)
			wantRemoved := []string{"container pg", "image reg/shop@sha256:aa"}
			wantDeleted := []string{"container apptown-svc-env-shop-main-postgres", "image reg/shop@sha256:aa"}
			if backedUp {
				wantRemoved = []string{"container pg", "volume anon-pg", "image reg/shop@sha256:aa"}
				wantDeleted = []string{"container apptown-svc-env-shop-main-postgres", "volume anon-pg", "image reg/shop@sha256:aa"}
			}
			if !reflect.DeepEqual(h.removed, wantRemoved) || !reflect.DeepEqual(out.Deleted, wantDeleted) {
				t.Fatalf("removed %v deleted %v", h.removed, out.Deleted)
			}
			var left []string
			for _, l := range out.Left {
				left = append(left, fmt.Sprintf("%s data=%v", l.What, l.Data))
				if l.Why == "" {
					t.Errorf("%s left without a reason", l.What)
				}
			}
			wantLeft := []string{"network apptown-net-env-shop-main data=false"}
			if !backedUp {
				wantLeft = append(wantLeft, "volume anon-pg data=true")
			}
			wantLeft = append(wantLeft, "volume unreachable data=true")
			wantLeft = append(wantLeft, "bucket b data=false")
			if !reflect.DeepEqual(left, wantLeft) {
				t.Fatalf("left %v", left)
			}
			if !h.objects["container someone-elses"] || !h.objects["volume not-listed"] || !h.objects["volume unreachable"] {
				t.Fatal("something not on the list, or not known to be there, was deleted")
			}
		})
	}
}

// A synthetic need whose parameters moved is reset only when the
// deployment recorded making its data volume. A volume that was there
// before (reused, never recorded) is someone's data: the reset is refused
// and neither the container nor the volume is touched.
func TestASyntheticResetDeletesOnlyARecordedNeedVolume(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprintf("recorded %v", recorded), func(t *testing.T) {
			t.Parallel()
			cname := Current.Service("env-shop-main", "postgres")
			vol := Current.NeedVolume("env-shop-main", "postgres")
			h := newOwnedHost("container "+cname, "volume "+vol, "network apptown-net-env-shop-main")
			dk := Scope{Client: &fakeClient{reply: func(req Command) ([]byte, error) {
				if line := strings.Join(req.Args, " "); strings.Contains(line, "{{.Config.Image}}") {
					return []byte("postgres:15\n"), nil // made from another image than declared now
				}
				return h.reply(req)
			}}}
			own := Ownership{Record: func(deploy.Owned) error { return nil }, Needs: map[string]string{"postgres": "/var/lib/postgresql/data"}}
			if recorded {
				own.priorResources = []deploy.Owned{{Kind: "volume", ID: vol, Data: true, Need: "postgres"}}
			}
			_, err := EnsureStandingServices(context.Background(), dk, "env-shop-main", []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16"}}, "synthetic", own)
			if !recorded {
				if err == nil || !strings.Contains(err.Error(), "not one this deployment recorded making") {
					t.Fatalf("an unrecorded volume was reset: %v", err)
				}
				if len(h.removed) != 0 {
					t.Fatalf("a refused reset removed %v", h.removed)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(h.removed, "volume "+vol) || !slices.Contains(h.removed, "container "+cname) {
				t.Fatalf("the recorded synthetic volume was not reset: %v", h.removed)
			}
		})
	}
}

// A standing deployment's network, need volume and service containers say,
// for a person reading `docker ps` and `docker volume ls`, which project and
// deployment they are for (dockerlabel.Informational).
//
// Kills: the informational labels left off any of them.
func TestAStandingStackSaysWhichProjectAndDeploymentItIsFor(t *testing.T) {
	t.Parallel()
	h := newOwnedHost()
	info := dockerlabel.Informational("shop", "main")
	own := Ownership{Needs: map[string]string{"postgres": "/var/lib/postgresql/data"}, info: info}
	if _, err := EnsureStandingServices(context.Background(), h.scope(), "dep-3b89d1da8075", []deploy.ServiceSpec{{Name: "postgres", Image: "postgres:16"}}, "synthetic", own); err != nil {
		t.Fatal(err)
	}
	want := strings.Join(info, " ")
	for _, c := range h.created {
		if !strings.Contains(c, want) {
			t.Errorf("made without saying whose it is: %s", c)
		}
	}
	if len(h.created) != 2 || len(h.runs) != 1 || !strings.Contains(strings.Join(h.runs[0], " "), want) {
		t.Fatalf("made %v, ran %v", h.created, h.runs)
	}
}

// A container is recorded by the ID Docker printed for it, never by its
// name: output without an ID is an error, so nothing a take-down would
// remove is recorded under a name another container can take.
func TestACreatedContainerIsRecordedOnlyByItsPrintedID(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("ab", 32)
	if got, err := createdID([]byte("Unable to find image locally\n"+id+"\n"), "apptown-shop-main"); err != nil || got != id {
		t.Fatalf("the printed ID was not taken: %q %v", got, err)
	}
	for _, out := range []string{"", "WARNING: something\n", "apptown-shop-main\n"} {
		if got, err := createdID([]byte(out), "apptown-shop-main"); err == nil || got != "" {
			t.Fatalf("output %q gave the identity %q", out, got)
		}
	}
}

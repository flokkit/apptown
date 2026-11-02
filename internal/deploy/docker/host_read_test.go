package docker_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// A fleet collection has a constant command budget, not a command per container.
func TestReadHostCollectsAllContainersInOneInspection(t *testing.T) {
	t.Parallel()
	d := dockerfake.New()
	for i := 0; i < 10; i++ {
		d.SetRunning(fmt.Sprintf("apptown-dep-%d", i), true)
	}
	dk := docker.Scope{Client: d}
	q := docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true, Use: true, ReaderImage: "df:test", ImageRefs: []string{"app:test"}}
	// Warm the standing reader, then count the exact completed collection.
	if _, err := docker.ReadHost(t.Context(), dk, q); err != nil {
		t.Fatal(err)
	}
	d.ResetCommands()
	f, err := docker.ReadHost(t.Context(), dk, q)
	if err != nil {
		t.Fatal(err)
	}
	if f.Containers.Value == nil || len(*f.Containers.Value) != 10 {
		t.Fatalf("containers: %+v", f.Containers)
	}
	var commands []string
	for _, c := range d.Commands() {
		commands = append(commands, c[0])
	}
	want := []string{"info", "ps", "inspect", "volume", "network", "image", "ps", "exec", "cp", "exec"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands %v, want %v", d.Commands(), want)
	}
}

func TestReadHostKeepsOtherSectionsWhenOneFails(t *testing.T) {
	t.Parallel()
	d := dockerfake.New()
	d.SetRunning("apptown-dep-one", true)
	d.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "volume" {
			return nil, errors.New("unread volumes"), true
		}
		return nil, nil, false
	})
	f, err := docker.ReadHost(t.Context(), docker.Scope{Client: d}, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true})
	if err == nil || f.Resources.Value != nil || f.Resources.Reason == "" {
		t.Fatalf("unread section: %+v %v", f.Resources, err)
	}
	if f.Containers.Value == nil || len(*f.Containers.Value) != 1 || f.Host.Value == nil || f.Images.Value == nil {
		t.Fatalf("partial evidence lost: %+v", f)
	}
}

type disappearingHost struct{ unanswered, legacy bool }

func (d disappearingHost) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) {
	switch c.Args[0] {
	case "ps":
		return []byte("old\nkept"), nil
	case "inspect":
		fmt.Fprintln(c.Stdout, `{"Id":"kept","Name":"/kept","State":{"Running":true}}`)
		if d.unanswered {
			return nil, nil
		}
		if d.legacy {
			fmt.Fprintln(c.Stderr, "Error: No such object: old")
		} else {
			fmt.Fprintln(c.Stderr, "Error response from daemon: No such container: old")
		}
		return nil, errors.New("exit 1")
	}
	return nil, errors.New("unexpected command")
}
func TestReadHostAcceptsOnlyExplicitlyAbsentContainers(t *testing.T) {
	t.Parallel()
	for _, host := range []disappearingHost{{}, {legacy: true}, {unanswered: true}} {
		f, err := docker.ReadHost(t.Context(), docker.Scope{Client: host}, docker.HostQuery{Containers: true})
		if host.unanswered {
			if err == nil || !strings.Contains(err.Error(), "old") || f.Containers.Value != nil {
				t.Fatalf("unanswered container became absent: %+v %v", f, err)
			}
		} else if err != nil || len(*f.Containers.Value) != 1 {
			t.Fatalf("removed container: %+v %v", f, err)
		}
	}
}

// Both readers identify and classify the same resources, ownership and daemon.
// Inspect supplies the literal state in Detail; ps supplies Docker's elapsed text.
func TestHostInventoryMatchesProviderInventory(t *testing.T) {
	t.Parallel()
	d := dockerfake.New()
	d.SetRunning("apptown-dep-one", true)
	d.SetRunning("foreign", true)
	d.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "info" && args[2] == "{{.ID}}" {
			return []byte("fake-daemon"), nil, true
		}
		if args[0] == "ps" && args[1] == "-a" {
			return []byte("apptown-dep-one\tUp 3 minutes\tsha256:built\tteam\tstack\tours\n"), nil, true
		}
		if args[0] == "volume" {
			return []byte("apptown-data-one\tteam\tstack\tours\nanonymous\t\t\t\n"), nil, true
		}
		if args[0] == "network" {
			return []byte("apptown-net-one\tteam\tstack\tours\n"), nil, true
		}
		return nil, nil, false
	})
	if _, err := d.Run(t.Context(), "", docker.Command{Args: []string{"run", "--name", "apptown-dep-one", "--label", "apptown.owner=team", "--label", "apptown.instance=ours", "--label", "com.docker.compose.project=stack", "sha256:built"}}); err != nil {
		t.Fatal(err)
	}
	dk := docker.Scope{Client: d, Instance: "ours"}
	refs := []string{"app:test"}
	old, err := (docker.Inventory{Scope: func(context.Context) (docker.Scope, error) { return dk, nil }, Images: func(context.Context) ([]string, error) { return refs, nil }}).Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	facts, err := docker.ReadHost(t.Context(), dk, docker.HostQuery{Host: true, Containers: true, Resources: true, Images: true, ImageRefs: refs})
	if err != nil {
		t.Fatal(err)
	}
	got := docker.HostInventory(facts, "ours", nil)
	for i := range old {
		old[i].Detail = ""
	}
	for i := range got {
		got[i].Detail = ""
	}
	if !reflect.DeepEqual(got, old) {
		t.Fatalf("inventory differs:\n got %+v\nwant %+v", got, old)
	}
}

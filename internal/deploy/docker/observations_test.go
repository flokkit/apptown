package docker_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

// Both paths report the same classification, in placement order, including
// a labelled generation that differs from the recorded placement.
func TestFleetAndSingleReadsShareContainerClassification(t *testing.T) {
	t.Parallel()
	daemon := dockerfake.New()
	scope := func(context.Context) (docker.Scope, error) { return docker.Scope{Client: daemon}, nil }
	ps := []deploy.Placement{}
	for n, tc := range []struct {
		name             string
		running, stopped bool
		generation       string
	}{
		{"running", true, false, "3"}, {"exited", false, false, "3"}, {"stopped", false, true, "3"}, {"replaced", true, false, "4"},
	} {
		if _, err := daemon.Run(t.Context(), "", docker.Command{Args: []string{"run", "-d", "--name", tc.name, "--label", "apptown.generation=" + tc.generation, "app"}}); err != nil {
			t.Fatal(err)
		}
		if !tc.running {
			if _, err := daemon.Run(t.Context(), "", docker.Command{Args: []string{"stop", tc.name}}); err != nil {
				t.Fatal(err)
			}
		}
		ps = append(ps, deploy.Placement{Container: tc.name, Stopped: tc.stopped, Generation: 3, URL: fmt.Sprint("https://app/", n), Handle: map[string]any{"container": tc.name}})
	}
	ps = append(ps, deploy.Placement{Container: "missing", Generation: 3}, deploy.Placement{Generation: 3})
	inv := docker.Inventory{Scope: scope}
	got, err := inv.ObserveAll(t.Context(), ps)
	if err != nil || len(got) != len(ps) {
		t.Fatalf("fleet %+v: %v", got, err)
	}
	if calls := len(daemon.Matching("inspect ")); calls != 1 {
		t.Fatalf("host reads %d", calls)
	}
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) == 4 && args[0] == "inspect" && args[3] == "missing" {
			return []byte("Error: No such object: missing"), fmt.Errorf("exit status 1"), true
		}
		return nil, nil, false
	})
	driver := docker.Driver{Scope: scope}
	for n, p := range ps {
		single, err := driver.Observe(t.Context(), p)
		if err != nil || !reflect.DeepEqual(single, got[n]) {
			t.Fatalf("%s single %+v fleet %+v: %v", p.Container, single, got[n], err)
		}
	}
	for n, want := range []struct {
		rollout, health string
		gen             int64
	}{
		{"active", "healthy", 3}, {"absent", "unhealthy", 3}, {"stopped", "unknown", 3}, {"active", "healthy", 4}, {"absent", "unknown", 3}, {"absent", "unknown", 3},
	} {
		if o := got[n]; o.Rollout != want.rollout || o.Health != want.health || o.Generation != want.gen {
			t.Fatalf("%d: %+v want %+v", n, o, want)
		}
	}
}

func TestFailedFleetReadingNeverPublishesPartialAnswers(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"unavailable", "malformed", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			daemon := dockerfake.New()
			daemon.SetCommandReply(func(req docker.Command) ([]byte, error, bool) {
				fmt.Fprintln(req.Stdout, `"/good"`+"\t"+`{"id":"good","running":true,"labels":{"apptown.generation":"3"}}`)
				switch scenario {
				case "unavailable":
					return nil, fmt.Errorf("daemon unavailable"), true
				case "malformed":
					fmt.Fprintln(req.Stdout, "not json")
				}
				return nil, nil, true
			})
			inv := docker.Inventory{Scope: func(context.Context) (docker.Scope, error) { return docker.Scope{Client: daemon}, nil }}
			if got, err := inv.ObserveAll(t.Context(), []deploy.Placement{{Container: "good", Generation: 3}, {Container: "app", Generation: 3}}); err == nil || got != nil {
				t.Fatalf("partial answer %+v: %v", got, err)
			}
		})
	}
}

// One container's untrusted identity cannot hide its neighbours' readings.
func TestInvalidGenerationOnlyBlindsItsOwnDeployment(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"", "bad", "0", "-1"} {
		t.Run(label, func(t *testing.T) {
			daemon := dockerfake.New()
			for _, name := range []string{"good", "bad"} {
				generation := "3"
				if name == "bad" {
					generation = label
				}
				if _, err := daemon.Run(t.Context(), "", docker.Command{Args: []string{"run", "-d", "--name", name, "--label", "apptown.generation=" + generation, "app"}}); err != nil {
					t.Fatal(err)
				}
			}
			inv := docker.Inventory{Scope: func(context.Context) (docker.Scope, error) { return docker.Scope{Client: daemon}, nil }}
			got, err := inv.ObserveAll(t.Context(), []deploy.Placement{{Container: "good", Generation: 3}, {Container: "bad", Generation: 3}})
			if err != nil || len(got) != 2 {
				t.Fatalf("fleet %+v: %v", got, err)
			}
			if got[0].Rollout != "active" || got[0].Generation != 3 || got[1].Rollout != "" || got[1].Generation != 0 {
				t.Fatalf("fleet %+v", got)
			}
		})
	}
}

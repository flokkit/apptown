package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/dockerlabel"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type runtimeDaemon struct {
	states map[string]Runtime
	acts   []string
}

func (f *runtimeDaemon) Run(_ context.Context, _ string, c Command) ([]byte, error) {
	a := c.Args
	if a[0] == "inspect" {
		r, ok := f.states[a[len(a)-1]]
		if !ok {
			return []byte("Error: No such object: " + a[len(a)-1]), fmt.Errorf("absent")
		}
		return json.Marshal(r)
	}
	if a[0] == "start" || a[0] == "stop" {
		r := f.states[a[1]]
		r.Running = a[0] == "start"
		f.states[a[1]] = r
		f.acts = append(f.acts, a[0]+":"+a[1])
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command: %v", a)
}
func TestRuntimeControlsOnlyAppContainersAndRestartStopsThenStarts(t *testing.T) {
	for _, action := range []string{"stop", "start", "restart"} {
		t.Run(action, func(t *testing.T) {
			daemon := &runtimeDaemon{states: map[string]Runtime{
				"public": {ID: "public", Running: action != "start", Labels: map[string]string{dockerlabel.Deployment: "7"}},
				"worker": {ID: "worker", Running: action != "start"}, "db": {ID: "db", Running: true}}}
			d := &Driver{Scope: func(context.Context) (Scope, error) { return Scope{Client: daemon}, nil }, Owned: func(context.Context) ([]deploy.Owned, error) {
				return []deploy.Owned{{Kind: "container", ID: "public"}, {Kind: "container", ID: "worker"}, {Kind: "container", ID: "db", Need: "postgres"}, {Kind: "volume", ID: "data"}}, nil
			}}
			if err := d.Runtime(t.Context(), deploy.Placement{Label: "7", Container: "public"}, action); err != nil {
				t.Fatal(err)
			}
			want := []string{action + ":worker", action + ":public"}
			if action == "restart" {
				want = []string{"stop:worker", "stop:public", "start:worker", "start:public"}
			}
			if !reflect.DeepEqual(daemon.acts, want) || !daemon.states["db"].Running {
				t.Fatalf("acts=%v database=%+v", daemon.acts, daemon.states["db"])
			}
		})
	}
}

func TestStoppedReconcilePreparesCodeWithoutStarting(t *testing.T) {
	daemon := &fakeDaemon{}
	boot := &stoppedBoot{fakeBoot: fakeBoot{daemon: daemon}}
	d := &Driver{Allowed: AuthorisedWhenRequested, ServingLabel: "stable", Scope: func(context.Context) (Scope, error) { return Scope{Client: daemon}, nil }, Boot: boot, URL: func(string, string, int) (string, error) { return "https://app.example", nil }}
	c := mustRuntimeContract(t)
	body := []byte(`{"version":1,"revision":"two"}`)
	art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)), Source: &deploy.Source{Contract: c, Revision: "two"}}
	o, err := d.Reconcile(t.Context(), deploy.Placement{ID: idstest.N(7), Label: "7", Name: "main", Stopped: true}, 2, art, "release", func(string, ...any) {}, func(map[string]any) error { return nil })
	if err != nil || o.Rollout != "stopped" || !boot.req.stopped || daemon.running {
		t.Fatalf("stopped reconcile: %+v %v request=%+v", o, err, boot.req)
	}
}

type stoppedBoot struct{ fakeBoot }

func (b *stoppedBoot) Boot(ctx context.Context, req BootRequest, c *appcontract.Contract) (BootResult, error) {
	r, err := b.fakeBoot.Boot(ctx, req, c)
	b.daemon.running = !req.stopped
	return r, err
}
func mustRuntimeContract(t *testing.T) *appcontract.Contract {
	t.Helper()
	c, err := appcontract.Parse([]byte("app:\n  web:\n    image: example/web\n    public: true\n    ports: [\"8080\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

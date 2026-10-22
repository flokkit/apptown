package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// A placement run from source on a fake daemon, then observed, resumed
// and torn down: the driver with nothing around it.

// fakeDaemon answers the few questions the driver asks a Docker daemon.
type fakeDaemon struct {
	calls   [][]string
	exists  bool
	running bool
	image   string
	labels  map[string]string
}

func (f *fakeDaemon) Run(_ context.Context, _ string, req Command) ([]byte, error) {
	f.calls = append(f.calls, req.Args)
	if out, ok := roomyDisk(req.Args); ok {
		return out, nil
	}
	args := req.Args
	switch {
	case args[0] == "inspect" && args[1] == "--format": // identity and liveness
		name := args[len(args)-1]
		if !f.exists {
			return []byte("Error: No such object: " + name), errors.New("exit status 1")
		}
		return json.Marshal(Runtime{ID: "container-id", Image: f.image, Running: f.running, Labels: f.labels})
	case args[0] == "inspect" && args[1] == "-f": // running?
		return []byte(fmt.Sprint(f.exists && f.running)), nil
	case args[0] == "rm":
		f.exists = false
		return nil, nil
	}
	return nil, nil
}

func (f *fakeDaemon) did(verb string) bool {
	for _, c := range f.calls {
		if c[0] == verb {
			return true
		}
	}
	return false
}

// fakeBoot brings the container up with the identity the request names.
type fakeBoot struct {
	daemon *fakeDaemon
	req    BootRequest
}

func (b *fakeBoot) Boot(_ context.Context, req BootRequest, _ *appcontract.Contract) (BootResult, error) {
	b.req = req
	b.daemon.exists, b.daemon.running, b.daemon.image = true, true, "sha256:game"
	b.daemon.labels = req.Identity.Labels()
	return BootResult{HostPort: 41233, Stack: ResolvedStack{Plan: &appcontract.Plan{
		Parts: []appcontract.Part{{Name: "game", Public: true, Image: "sha256:game"}}}}}, nil
}

func TestDriverRunsAPlacementFromSource(t *testing.T) {
	t.Parallel()
	daemon := &fakeDaemon{}
	boot := &fakeBoot{daemon: daemon}
	var released []string
	d := &Driver{
		Scope:        func(context.Context) (Scope, error) { return Scope{Client: daemon}, nil },
		Boot:         boot,
		URL:          func(_, _ string, port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) },
		AfterRelease: func(p deploy.Placement) { released = append(released, p.Name) },
		DataClass:    "production",
	}
	c, err := appcontract.Parse([]byte("app:\n  game:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":1,"revision":"abc"}`)
	art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)),
		Source: &deploy.Source{Workspace: t.TempDir(), Revision: "abc", Slug: "game", Contract: c}}
	place := deploy.Placement{ID: idstest.N(7), Label: "7", Name: "main"}
	ctx := context.Background()
	quiet := func(string, ...any) {}

	noted := map[string]any{}
	o, err := d.Reconcile(ctx, place, 1, art, "key", quiet, func(v map[string]any) error {
		for k, val := range v {
			noted[k] = val
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.Rollout != "active" || o.Health != "healthy" || o.Generation != 1 || o.URL != "http://127.0.0.1:41233" || o.ImageDigest != "sha256:game" {
		t.Fatalf("observation after boot: %+v", o)
	}
	name := ByKey(idstest.N(7)).App()
	if boot.req.Name != name || boot.req.Deployment != "main" || !boot.req.Persistent || boot.req.Identity.Deployment != "7" || boot.req.Revision != "abc" || boot.req.DataClass != "production" {
		t.Fatalf("the boot request names the placement and the revision it serves: %+v", boot.req)
	}
	if noted["container"] != name || noted["dockerReady"] == nil {
		t.Fatalf("checkpoints carry the container and the boot proof: %v", noted)
	}
	if len(released) != 1 || released[0] != "main" {
		t.Fatalf("what follows a release is told the placement: %v", released)
	}

	// Resume from what was noted: the proof is verified against the host,
	// and current health is not invented.
	place.Container, place.Generation, place.URL = name, 1, o.URL
	resumed, err := Resume(ctx, Scope{Client: daemon}, place, 1, noted)
	if err != nil || resumed.Rollout != "active" || resumed.Health != "unknown" {
		t.Fatalf("resume: %+v err=%v", resumed, err)
	}

	if o, err := d.Observe(ctx, place); err != nil || o.Rollout != "active" || o.Health != "healthy" {
		t.Fatalf("observe while running: %+v err=%v", o, err)
	}
	if running, err := d.Inspect(ctx, place); err != nil || !running {
		t.Fatalf("inspect while running: %v err=%v", running, err)
	}
	daemon.running = false
	if o, err := d.Observe(ctx, place); err != nil || o.Rollout != "absent" || o.Health != "unhealthy" {
		t.Fatalf("observe when stopped: %+v err=%v", o, err)
	}

	if _, err := d.Teardown(ctx, place, 2, deploy.TakeDown{}); err == nil || !strings.Contains(err.Error(), "not 2") {
		t.Fatalf("teardown refuses a generation that was not recorded: %v", err)
	}
	if _, err := d.Teardown(ctx, place, 1, deploy.TakeDown{}); err != nil || daemon.exists || !daemon.did("rm") {
		t.Fatalf("teardown removes the container: err=%v exists=%v", err, daemon.exists)
	}
	if running, err := d.Inspect(ctx, place); err != nil || running {
		t.Fatalf("an absent container is not running, not an error: %v err=%v", running, err)
	}
}

func TestDriverRefusesAnythingButPreparedSource(t *testing.T) {
	t.Parallel()
	d := &Driver{Scope: func(context.Context) (Scope, error) { return Scope{Client: &fakeDaemon{}}, nil }}
	for _, art := range []deploy.Artifact{
		{Kind: "compose", Body: []byte("services: {}")},
		{Kind: "docker-source", Body: []byte(`{}`), Hash: "stale"},
		{Kind: "docker-source", Body: []byte(`{}`), Hash: deploy.ConfigHash(`{}`)}, // no source
	} {
		_, err := d.Reconcile(context.Background(), deploy.Placement{HostPrefix: "apptown-", ID: idstest.N(1), Label: "1", Name: "main"}, 1, art, "key", func(string, ...any) {}, func(map[string]any) error { return nil })
		if !deploy.IsTerminal(err) {
			t.Fatalf("%s: %v", art.Kind, err)
		}
	}
}

type removalDaemon struct {
	runtime               Runtime
	inspectErr, removeErr error
	removed               string
	rmArgs                []string
	others                [][]string
}

func (f *removalDaemon) Run(_ context.Context, _ string, req Command) ([]byte, error) {
	switch req.Args[0] {
	case "inspect":
		if len(req.Args) != 4 {
			break
		}
		if f.inspectErr != nil {
			return nil, f.inspectErr
		}
		return json.Marshal(f.runtime)
	case "rm":
		f.removed, f.rmArgs = req.Args[len(req.Args)-1], req.Args
		return nil, f.removeErr
	}
	f.others = append(f.others, req.Args)
	return nil, errors.New("unexpected command")
}
func TestTeardownUsesVerifiedContainerIDAndPropagatesFailures(t *testing.T) {
	for _, scenario := range []string{"success", "replacement", "unlabelled", "no-id", "inspect-error", "remove-error", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := &removalDaemon{runtime: Runtime{ID: "old-immutable-id", Labels: deploy.DockerIdentity{Deployment: "7", Generation: 3}.Labels()}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "replacement":
				f.runtime.Labels["apptown.generation"] = "4"
			case "unlabelled":
				f.runtime.Labels = nil
			case "no-id":
				f.runtime.ID = ""
			case "inspect-error":
				f.inspectErr = errors.New("daemon unavailable")
			case "remove-error":
				f.removeErr = errors.New("deletion refused")
			case "cancelled":
				cancel()
			}
			d := Driver{Scope: func(context.Context) (Scope, error) { return Scope{Client: f}, nil }}
			owned := []deploy.Owned{{Kind: "network", ID: "apptown-net-env-shop-main"}}
			_, err := d.Teardown(ctx, deploy.Placement{HostPrefix: "apptown-", ID: idstest.N(7), Label: "7", Generation: 3, Container: "reusable-name"}, 3, deploy.TakeDown{Owned: owned})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("teardown: %v", err)
			}
			// The rest of the list is touched only once the serving container is gone.
			if (len(f.others) > 0) != (scenario == "success") {
				t.Fatalf("the rest of the list after %s: %v", scenario, f.others)
			}
			if slices.Contains(f.rmArgs, "-v") {
				t.Fatalf("the serving container was removed with its unlisted volumes: %v", f.rmArgs)
			}
			if scenario == "success" || scenario == "remove-error" {
				if f.removed != "old-immutable-id" {
					t.Fatalf("removed name or wrong identity: %q", f.removed)
				}
			} else if f.removed != "" {
				t.Fatalf("refused selection removed %q", f.removed)
			}
		})
	}
}

func TestObserveDistinguishesUnavailableFromStopped(t *testing.T) {
	for _, scenario := range []string{"running", "stopped", "absent", "unavailable", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			daemon := &fakeDaemon{exists: scenario != "absent", running: scenario == "running"}
			var client Client = daemon
			if scenario == "unavailable" {
				client = &removalDaemon{inspectErr: errors.New("daemon unavailable")}
			}
			if scenario == "cancelled" {
				cancel()
			}
			d := Driver{Scope: func(context.Context) (Scope, error) { return Scope{Client: client}, nil }}
			got, err := d.Observe(ctx, deploy.Placement{HostPrefix: "apptown-", Container: "app", Generation: 3})
			if scenario == "unavailable" || scenario == "cancelled" {
				if err == nil {
					t.Fatal("failed observation became stopped")
				}
				return
			}
			expected := "absent"
			if scenario == "running" {
				expected = "active"
			}
			if err != nil || got.Rollout != expected {
				t.Fatalf("observation: %+v %v", got, err)
			}
		})
	}
}

// A boot that failed before touching Docker is a known outcome: the driver
// says so, so the attempt settles failed and a fixed cause can be retried. A
// failure after effects stays unknown.
func TestADriverBootThatChangedNothingIsAKnownFailure(t *testing.T) {
	t.Parallel()
	for phase, terminal := range map[string]bool{PhaseResolve: true, PhaseSecrets: true, PhaseConfig: true, PhaseNeeds: false, PhaseParts: false, PhaseApp: false} {
		t.Run(phase, func(t *testing.T) {
			d := &Driver{
				Scope: func(context.Context) (Scope, error) { return Scope{Client: &fakeDaemon{}}, nil },
				Boot:  failingBoot{&BootError{Phase: phase, Err: errors.New("it failed")}},
				URL:   func(_, _ string, port int) string { return "" },
			}
			c, err := appcontract.Parse([]byte("app:\n  game:\n    static: .\n    public: true\n"))
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"version":1,"revision":"abc"}`)
			art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)),
				Source: &deploy.Source{Workspace: t.TempDir(), Revision: "abc", Slug: "game", Contract: c}}
			_, err = d.Reconcile(context.Background(), deploy.Placement{HostPrefix: "apptown-", ID: idstest.N(7), Label: "7", Name: "main"}, 1, art, "key", func(string, ...any) {}, func(map[string]any) error { return nil })
			if err == nil || deploy.IsTerminal(err) != terminal {
				t.Fatalf("terminal=%v, got %v (terminal=%v)", terminal, err, deploy.IsTerminal(err))
			}
		})
	}
}

// A need Docker called unhealthy started nothing of the app: the boot's
// needs-phase error carries that verdict through Reconcile as known.
func TestADriverBootWhoseNeedDockerCalledUnhealthyIsAKnownFailure(t *testing.T) {
	t.Parallel()
	d := &Driver{
		Scope: func(context.Context) (Scope, error) { return Scope{Client: &fakeDaemon{}}, nil },
		Boot:  failingBoot{&BootError{Phase: PhaseNeeds, Err: deploy.Terminal(errors.New("the postgres service is unhealthy"))}},
		URL:   func(_, _ string, port int) string { return "" },
	}
	c, err := appcontract.Parse([]byte("app:\n  game:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":1,"revision":"abc"}`)
	art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)),
		Source: &deploy.Source{Workspace: t.TempDir(), Revision: "abc", Slug: "game", Contract: c}}
	_, err = d.Reconcile(context.Background(), deploy.Placement{ID: idstest.N(7), Label: "7", Name: "main"}, 1, art, "key", func(string, ...any) {}, func(map[string]any) error { return nil })
	if err == nil || !deploy.IsTerminal(err) {
		t.Fatalf("an unhealthy need's boot is not a known failure: %v", err)
	}
}

type failingBoot struct{ err error }

func (b failingBoot) Boot(context.Context, BootRequest, *appcontract.Contract) (BootResult, error) {
	return BootResult{}, b.err
}

// A source that names its images (a release recorded on this daemon) boots
// them; a source that names none is prepared from source, as before.
func TestDriverBootsTheImagesItsSourceNames(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  game:\n    static: .\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":1,"revision":"abc"}`)
	for _, images := range []map[string]string{nil, {"game": "sha256:game"}} {
		daemon := &fakeDaemon{}
		boot := &fakeBoot{daemon: daemon}
		d := &Driver{Scope: func(context.Context) (Scope, error) { return Scope{Client: daemon}, nil }, Boot: boot,
			URL: func(_, _ string, port int) string { return fmt.Sprint(port) }, AfterRelease: func(deploy.Placement) {}}
		art := deploy.Artifact{Kind: "docker-source", Body: body, Hash: deploy.ConfigHash(string(body)),
			Source: &deploy.Source{Workspace: t.TempDir(), Revision: "abc", Slug: "game", Contract: c, Images: images, Init: map[string]string{}}}
		if _, err := d.Reconcile(context.Background(), deploy.Placement{HostPrefix: "apptown-", ID: idstest.N(7), Label: "7", Name: "main"}, 1, art, "key", func(string, ...any) {}, func(map[string]any) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if (boot.req.Recorded != nil) != (images != nil) || (images != nil && boot.req.Recorded.Parts["game"] != "sha256:game") {
			t.Fatalf("images %v: boot request %+v", images, boot.req.Recorded)
		}
	}
}

// A recorded image the daemon no longer has fails the boot before any
// effect: the attempt's outcome is known.
func TestARecordedBootWithAMissingImageChangesNothing(t *testing.T) {
	t.Parallel()
	c, err := appcontract.Parse([]byte("app:\n  game:\n    image: example/game:1\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeClient{reply: func(req Command) ([]byte, error) {
		if req.Args[0] == "image" {
			return []byte("Error: No such image"), errors.New("exit status 1")
		}
		return nil, nil
	}}
	_, err = NewHost(Dependencies{Needs: reviewNoNeeds{}, Secrets: reviewNoSecrets{}}).Boot(context.Background(), BootRequest{Name: ByKey(idstest.N(7)).App(), StackID: ByKey(idstest.N(7)).Stack(), Workspace: t.TempDir(), Docker: Scope{Client: fake}, Persistent: true, Names: Current,
		Identity: &deploy.DockerIdentity{Deployment: "7", Generation: 2}, Recorded: &RecordedImages{Parts: map[string]string{"game": "sha256:gone"}}}, c)
	if !errors.Is(err, ErrRecordedImageMissing) || !AsBootError(err).BeforeEffects() {
		t.Fatalf("a missing recorded image: %v", err)
	}
	for _, args := range fake.args {
		if args[0] != "image" && !diskRead(args) {
			t.Fatalf("a boot whose image is missing ran %v", args)
		}
	}
}

package preview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
)

func previewCommandIndex(calls []string, prefix string) int {
	for i, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return i
		}
	}
	return -1
}

func TestAppPortBindsLoopbackEvenWithPublicHost(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "192.0.2.10")
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0007-7000-8000-000000000000"))
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: projectID}, false); err != nil {
		t.Fatalf("boot with a public host set: %v", err)
	}
	calls := previewCommands(client)
	var app string
	for _, call := range calls {
		if strings.HasPrefix(call, "run -d --name "+docker.PreviewChangeName(idstest.N(7))) {
			app = call
		}
	}
	if !strings.Contains(app, "-p 127.0.0.1::8080") || strings.Contains(app, "0.0.0.0") {
		t.Fatalf("app port must bind loopback behind the proxy; got %q", app)
	}
}

func TestBootChangeWorktreeSequence(t *testing.T) {
	t.Parallel()
	s, db, client, dir, projectID := previewBootWorld(t, "")
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0007-7000-8000-000000000000"))
	out, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: projectID}, false)
	if err != nil {
		t.Fatalf("boot: %v\ncalls:\n%s", err, strings.Join(previewCommands(client), "\n"))
	}
	if out.URL == "" {
		t.Fatal("preview URL was not returned")
	}
	calls := previewCommands(client)
	service, init, worker, app := -1, -1, -1, -1
	for i, call := range calls {
		if !strings.HasPrefix(call, "run ") {
			continue
		}
		switch {
		case strings.Contains(call, "apptown-svc-") && strings.Contains(call, "-postgres"):
			service = i
		case strings.Contains(call, "apptown-init-") && strings.Contains(call, "-migrate"):
			init = i
		case strings.Contains(call, "apptown-svc-") && strings.Contains(call, "-worker"):
			worker = i
		case strings.Contains(call, docker.PreviewChangeName(idstest.N(7))):
			app = i
		}
	}
	if service < 0 || init <= service || worker <= init || app <= worker || previewCommandIndex(calls, "pull reg.example/app:1") < 0 || !strings.HasSuffix(calls[app], " reg.example/app:1") || previewCommandIndex(calls, "build ") >= 0 {
		t.Fatalf("wrong preview boot sequence: %v", calls)
	}
	// The app names its stack, which an idle stop removes with it.
	if !strings.Contains(calls[app], "--label "+docker.StackLabel+"="+docker.ChangeStackID(idstest.N(7))) {
		t.Fatalf("the preview's app does not name its stack: %s", calls[app])
	}
	client.ResetCommands()
	dbtest.FailNextStatement(t, db, "-- name: RecordPreview")
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: projectID}, false); err != nil {
		t.Fatal(err)
	}
	if len(client.Matching("run ")) != 0 {
		t.Fatal("a live preview must be reused, not rebooted")
	}
	stored, err := s.deployments.ByName(t.Context(), projectID, deployment.PreviewName(idstest.N(7)))
	if err != nil || stored.ObservedGeneration != 1 || stored.URL != out.URL {
		t.Fatalf("failed transient recording changed earlier receipt: %+v %v", stored, err)
	}
}

func TestBootFailureCarriesItsPhase(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	client.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "inspect" && args[len(args)-1] == docker.PreviewChangeName(idstest.N(9)) && strings.Contains(strings.Join(args, " "), "Running") {
			return []byte("false\n"), nil, true
		}
		if args[0] == "logs" && !client.IsDiskRead(args) {
			return []byte("panic: no DATABASE_URL\n"), nil, true
		}
		return nil, nil, false
	})
	_, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false)
	if err == nil {
		t.Fatal("a dead app must fail the boot")
	}
	if be := docker.AsBootError(err); be.Phase != docker.PhaseProbe || !strings.HasPrefix(err.Error(), "the app didn't come up: panic: no DATABASE_URL") {
		t.Fatalf("phase and message: %v", err)
	}
	calls := previewCommands(client)
	ran := previewCommandIndex(calls, "run -d --name "+docker.PreviewChangeName(idstest.N(9)))
	reaped := false
	for i := ran; i < len(calls) && ran >= 0; i++ {
		if calls[i] == "rm -f -v "+docker.PreviewChangeName(idstest.N(9)) {
			reaped = true
		}
	}
	if !reaped || previewCommandIndex(calls, "network rm "+docker.Current.Network(docker.ChangeStackID(idstest.N(9)))) < ran {
		t.Fatalf("dead preview not reaped after failed probe: %v", calls)
	}
	requireNothingOfPreview(t, client, idstest.N(9))
	client.ResetCommands()
	ws := filepath.Join(dir, "workspaces", "change-00000000-000a-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	contract := strings.Replace(resolvedStackFixture, "postgres: {}", "quantumdb: {}", 1)
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(contract), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = s.RehearseChange(context.Background(), Change{ID: idstest.N(10), ProjectID: projectID}, false)
	if err == nil || docker.AsBootError(err).Phase != docker.PhaseResolve || err.Error() != "this app needs quantumdb, which is not in the dependency catalog" {
		t.Fatalf("unknown need: %v", err)
	}
	if got := client.Matching("run "); len(got) != 0 {
		t.Fatalf("unknown need ran containers: %v", got)
	}
}

// A preview is reused only as the incarnation it is: its source and the
// digest of its prepared values, as its container is labelled. A container
// serving the same source with other prepared values (an attachment edited
// since) is reaped and booted again; the same incarnation is reused.
func TestPreviewReuseAsksTheContainerWhatItServes(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	ws := filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	name := docker.PreviewChangeName(idstest.N(9))
	served := docker.WorktreeFingerprint(ws) + "+an-older-digest"
	client.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "inspect" && args[len(args)-1] == name && strings.Contains(strings.Join(args, " "), "apptown.incarnation") {
			return []byte(served + "\n"), nil, true
		}
		return nil, nil, false
	})
	client.SetRunning(name, true)
	out, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false)
	if err != nil {
		t.Fatal(err)
	}
	calls := previewCommands(client)
	rm, run := previewCommandIndex(calls, "rm -f "+name), previewCommandIndex(calls, "run -d --name "+name)
	if rm < 0 || run < 0 || rm > run || !strings.Contains(calls[run], "apptown.incarnation="+out.Incarnation) ||
		out.Public == nil || out.Incarnation != docker.WorktreeFingerprint(ws)+"+"+out.Public.Digest() {
		t.Fatalf("a preview with other prepared values was not reaped then restamped (%s): %v", out.Incarnation, calls)
	}
	served = out.Incarnation
	client.ResetCommands()
	client.SetRunning(name, true)
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false); err != nil {
		t.Fatal(err)
	}
	if got := client.Matching("run "); len(got) != 0 {
		t.Fatalf("the same incarnation rebooted: %v", got)
	}
}

// Everything a boot reads is prepared before anything is removed: a restart
// whose preparation fails leaves the running preview as it is.
func TestAFailedPreparationLeavesThePreviewRunning(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	ws := filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte("version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  no-such-thing: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client.SetRunning(docker.PreviewChangeName(idstest.N(9)), true)
	if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, true); err == nil {
		t.Fatal("a boot with a need nothing provides was not refused")
	}
	if got := client.Matching("rm "); len(got) != 0 {
		t.Fatalf("a failed preparation removed the running preview: %v", got)
	}
}

// An image preview reports a registry failure without trying to build source.
func TestImageContractPreviewReportsPullFailure(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	client.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "pull" {
			return []byte("registry unavailable"), errors.New("exit status 1"), true
		}
		return nil, nil, false
	})
	ws := filepath.Join(dir, "workspaces", "change-00000000-0007-7000-8000-000000000000")
	if err := os.MkdirAll(filepath.Join(ws, ".apptown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(`app:
  web:
    image: registry.example/x@sha256:abc
    ports: ["8080"]
    public: true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(7), ProjectID: projectID}, false)
	if err == nil || !strings.Contains(err.Error(), "registry unavailable") {
		t.Fatalf("image pull error must reach the preview: %v", err)
	}
	if len(client.Matching("build ")) != 0 {
		t.Fatal("image preview tried to build source")
	}
}

// A reuse probe its caller cut short is unknown health, not a dead app:
// the running preview of the same incarnation is neither removed nor
// booted again, and the caller is told it went away.
func TestACancelledReuseProbeLeavesThePreviewRunning(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	ws := filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	name := docker.PreviewChangeName(idstest.N(9))
	served := ""
	var cancel context.CancelFunc
	client.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "inspect" && args[len(args)-1] == name && strings.Contains(strings.Join(args, " "), "apptown.incarnation") {
			return []byte(served + "\n"), nil, true
		}
		if args[0] == "port" && cancel != nil {
			cancel() // the caller goes away just as the probe starts
		}
		return nil, nil, false
	})
	out, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false)
	if err != nil {
		t.Fatal(err)
	}
	served = out.Incarnation
	client.ResetCommands()
	client.SetRunning(name, true)
	ctx, c := context.WithCancel(context.Background())
	defer c()
	cancel = c
	if _, err := s.RehearseChange(ctx, Change{ID: idstest.N(9), ProjectID: projectID}, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled reuse answered %v", err)
	}
	if got := append(client.Matching("rm "), client.Matching("run ")...); len(got) != 0 {
		t.Fatalf("a cancelled probe removed or rebooted the preview: %v", got)
	}
}

// A local preview's boot outlives the caller that asked for it, and so does
// its record: a caller that goes away while the app is started still leaves
// the running preview in the deployment inventory. Removing a preview is
// likewise finished: a caller that goes away once its container is removed
// still has its row forgotten, and so does the reaper's.
func TestAPreviewsRowFollowsItsEffectThoughTheCallerHasGone(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	ws := filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	name := docker.PreviewChangeName(idstest.N(9))
	var cancel context.CancelFunc
	client.SetReply(func(args []string) ([]byte, error, bool) {
		if cancel != nil && (strings.HasPrefix(strings.Join(args, " "), "run -d --name "+name) || args[0] == "rm") {
			cancel()
		}
		return nil, nil, false
	})
	ctx, c := context.WithCancel(context.Background())
	cancel = c
	_, _ = s.RehearseChange(ctx, Change{ID: idstest.N(9), ProjectID: projectID}, false)
	c()
	if _, err := s.deployments.ByName(context.Background(), projectID, deployment.PreviewName(idstest.N(9))); err != nil {
		t.Fatalf("a preview booted for a caller that went away is not recorded: %v", err)
	}
	ctx, c = context.WithCancel(context.Background())
	cancel = c
	if err := s.DiscardChangePreview(ctx, idstest.N(9)); err != nil {
		t.Fatalf("discarding for a caller that went away: %v", err)
	}
	c()
	if _, err := s.deployments.ByName(context.Background(), projectID, deployment.PreviewName(idstest.N(9))); err == nil {
		t.Fatal("a removed preview's row was kept")
	}
	if err := s.deployments.RecordPreview(context.Background(), projectID, idstest.N(9), idstest.N(1), name, 4000, "http://p"); err != nil {
		t.Fatal(err)
	}
	gone, done := context.WithCancel(context.Background())
	done()
	if err := s.deployments.ForgetPreviewContainer(gone, name); err != nil {
		t.Fatalf("forgetting a reaped container's row for a gone caller: %v", err)
	}
	if _, err := s.deployments.ByName(context.Background(), projectID, deployment.PreviewName(idstest.N(9))); err == nil {
		t.Fatal("a reaped container's row was kept")
	}
}

// A preview's address at a port this AppTown's Docker publishes answers on
// AppTown host only (the bind is loopback, above). With a public host,
// people reach AppTown from elsewhere, so the owner says so for a preview
// with no preview domain; a preview that is live carries the mark to the
// page. (Deployment's own test covers standing deployments.)
func TestAPreviewPortAddressWithAPublicHostIsMarkedForAppTownHostOnly(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		publicHost, previewDomain string
		hostOnly                  bool
	}{
		{publicHost: "forge.example", hostOnly: true},
		{publicHost: "forge.example", previewDomain: "preview.example", hostOnly: false},
		{publicHost: "", hostOnly: false},
	} {
		w := newWorld(t, c.publicHost, "")
		w.env.Routes.PreviewDomain, w.env.Routes.PreviewScheme = c.previewDomain, "https"
		if got := w.service(Dependencies{}).PreviewsHostOnly(); got != c.hostOnly {
			t.Errorf("public host %q, preview domain %q: previews host-only %v, want %v", c.publicHost, c.previewDomain, got, c.hostOnly)
		}
	}
	var runtime Runtime
	if got := runtime.Inspect(context.Background(), ChangeKey(idstest.N(7)), previewFacts{URL: "http://forge.example:40001", HostOnly: true}); got.State != Live || !got.HostOnly {
		t.Fatalf("a live host-only preview: %+v", got)
	}
}

// A boot that fails before its app starts leaves nothing either: the
// services and init it started go, with the network and the data volume.
func TestAFailedBootLeavesNothingOfThePreview(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	writePreviewBootFixture(t, filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000"))
	client.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "run" && strings.Contains(strings.Join(args, " "), "-worker") {
			return []byte("no room for the worker"), errors.New("exit status 125"), true
		}
		return nil, nil, false
	})
	_, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false)
	if be := docker.AsBootError(err); err == nil || be.Phase != docker.PhaseParts {
		t.Fatalf("a part that cannot start fails the boot at its phase: %v", err)
	}
	requireNothingOfPreview(t, client, idstest.N(9))
}

// requireNothingOfPreview fails unless change id's preview is wholly gone
// from the daemon: no container its stack owns runs, and its app, data
// volume and network were removed.
func requireNothingOfPreview(t *testing.T, client *dockerfake.Daemon, id ids.ID) {
	t.Helper()
	calls := previewCommands(client)
	name, stack := docker.PreviewChangeName(id), docker.ChangeStackID(id)
	for _, want := range []string{"rm -f -v " + name, "volume rm " + docker.Current.DataVolume(name), "network rm " + docker.Current.Network(stack)} {
		if previewCommandIndex(calls, want) < 0 {
			t.Fatalf("%q was not issued: %v", want, calls)
		}
	}
	for _, args := range client.Matching("run ") {
		call := strings.Join(args, " ")
		for i := range args {
			if args[i] == "--name" && i+1 < len(args) && client.Running(args[i+1]) && (args[i+1] == name || strings.Contains(call, "apptown.owner="+stack)) {
				t.Fatalf("%s of the preview still runs", args[i+1])
			}
		}
	}
}

// A run's preview boot that fails here leaves nothing of that step's
// preview either: its app, its data volume, its stack's services and
// network.
func TestAFailedRunPreviewBootLeavesNothingOfItsStep(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	ws := filepath.Join(dir, "workspaces", "run-00000000-0004-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	gitCommitAll(t, ws, "the run's work")
	client.SetReply(func(args []string) ([]byte, error, bool) {
		if args[0] == "run" && strings.Contains(strings.Join(args, " "), "-worker") {
			return []byte("no room for the worker"), errors.New("exit status 125"), true
		}
		return nil, nil, false
	})
	if _, err := s.RunPreview(context.Background(), claimedPreviewStep{projectID, idstest.N(4), idstest.N(11)}, RunInput{Source: deploy.Source{Workspace: ws, Revision: "HEAD", Slug: "p"}}); err == nil {
		t.Fatal("a part that cannot start must fail the boot")
	}
	calls := previewCommands(client)
	name, stack := docker.PreviewRunName(idstest.N(4)), docker.RunStepStackID(idstest.N(11))
	for _, want := range []string{"rm -f -v " + name, "volume rm " + docker.Current.DataVolume(name), "network rm " + docker.Current.Network(stack)} {
		if previewCommandIndex(calls, want) < 0 {
			t.Fatalf("%q was not issued: %v", want, calls)
		}
	}
	for _, args := range client.Matching("run ") {
		for i := range args {
			if args[i] == "--name" && i+1 < len(args) && client.Running(args[i+1]) {
				t.Fatalf("%s of the failed run preview still runs", args[i+1])
			}
		}
	}
}

// A preview larger than its host can give a container is refused before
// the running one is replaced: nothing removed, nothing started. A size
// for a need that runs no service is refused there too.
func TestAnOversizedPreviewLeavesTheRunningOneAlone(t *testing.T) {
	t.Parallel()
	for name, app := range map[string]string{
		"a part larger than the host": "version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    resources: { memory: 16G }\n",
		"a need larger than the host": "version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  postgres:\n    resources: { memory: 9G }\n",
		"a size for a key":            "version: 1\napp:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\nneeds:\n  llm:\n    resources: { memory: 1G }\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, client, dir, projectID := previewBootWorld(t, "")
			client.SetHostInfo(8<<30, 8, "/var/lib/docker")
			ws := filepath.Join(dir, "workspaces", "change-00000000-0009-7000-8000-000000000000")
			writePreviewBootFixture(t, ws)
			if err := os.WriteFile(filepath.Join(ws, ".apptown", "app.yaml"), []byte(app), 0o644); err != nil {
				t.Fatal(err)
			}
			client.SetRunning(docker.PreviewChangeName(idstest.N(9)), true)
			want := map[bool]string{true: "llm declares resources, but llm runs no service", false: "this Docker host has 8G"}[strings.Contains(name, "key")]
			if _, err := s.RehearseChange(context.Background(), Change{ID: idstest.N(9), ProjectID: projectID}, false); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("an oversized preview: %v, want a refusal naming %q", err, want)
			}
			if got := append(client.Matching("rm "), client.Matching("run ")...); len(got) != 0 {
				t.Fatalf("the refusal came after the host was touched: %v", got)
			}
		})
	}
}

// Local run previews use the same resolved sidecar caps as worker previews.
func TestALocalRunPreviewCapsItsSuppliedService(t *testing.T) {
	t.Parallel()
	s, _, client, dir, projectID := previewBootWorld(t, "")
	ws := filepath.Join(dir, "workspaces", "run-00000000-0004-7000-8000-000000000000")
	writePreviewBootFixture(t, ws)
	gitCommitAll(t, ws, "the run's work")
	c, err := docker.ReadWorkspaceContract(ws)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RunPreview(t.Context(), claimedPreviewStep{projectID, idstest.N(4), idstest.N(11)}, RunInput{Source: deploy.Source{Workspace: ws, Revision: "HEAD", Slug: "p", Contract: c}, Services: []deploy.ServiceSpec{{Name: "cache", Image: "redis:7"}}})
	if err != nil {
		t.Fatal(err)
	}
	name := docker.Current.Service(docker.RunStepStackID(idstest.N(11)), "cache")
	if memory, _ := client.Limits(name); memory != 512<<20 {
		t.Fatalf("local workflow sidecar cap: %s %d", name, memory)
	}
}

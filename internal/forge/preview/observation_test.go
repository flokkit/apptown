package preview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/deploy/docker/dockerfake"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

// observationService is the preview owner over a daemon, with no worker:
// what a change's preview button reads.
func observationService(t *testing.T, client docker.Client) *Service {
	t.Helper()
	db := testDB(t)
	targets, dir := deployment.NewTargets(db, nil), t.TempDir()
	return New(db, Dependencies{DataDir: dir, Hosts: deployment.NewHosts(targets, nil, deployment.HostSettings{Scope: docker.Scope{Client: client}, DataDir: dir}),
		Environment: deployment.NewEnvironment(targets, deployment.EnvironmentDependencies{})})
}

func TestPreviewObservationAndWireShape(t *testing.T) {
	t.Parallel()
	fake := dockerfake.New()
	s := observationService(t, fake)
	inProgress := true
	observe := func() Status { return s.ChangeStatus(context.Background(), ids.Nil, idstest.N(7), inProgress) }
	if got := observe(); got.State != None {
		t.Fatalf("missing current contract: %+v", got)
	}
	inProgress = false
	if got := observe(); got.State != Cold {
		t.Fatalf("historical snapshot remains bootable: %+v", got)
	}
	inProgress = true
	dir := filepath.Join(s.dataDir, "workspaces", "change-00000000-0007-7000-8000-000000000000", ".forge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("app:\n  web: {image: example/app:1, public: true, port: 8080}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := observe(); got.State != Cold {
		t.Fatalf("declared app: %+v", got)
	}
	fake.SetRunning(docker.PreviewChangeName(idstest.N(7)), true)
	got := observe()
	if got.State != Live || got.URL != s.env.Routes().PreviewURL("change-00000000-0007-7000-8000-000000000000", 43210) {
		t.Fatalf("provider URL: %+v", got)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 2 || wire["state"] != string(Live) || wire["url"] != got.URL {
		t.Fatalf("wire shape: %s", body)
	}
	boot, err := s.runtime.Begin(context.Background(), ChangeKey(idstest.N(7)))
	if err != nil {
		t.Fatal(err)
	}
	defer boot.Finish(nil)
	before := len(fake.Commands())
	if got := observe(); got.State != Starting || got.At.IsZero() {
		t.Fatalf("active boot: %+v", got)
	}
	if len(fake.Commands()) != before {
		t.Fatal("status queried provider during a boot")
	}
}

// absentDaemon is a forge with no Docker of its own; asking it fails the test.
type absentDaemon struct {
	docker.Refusing
	t *testing.T
}

func (d absentDaemon) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) {
	d.t.Errorf("the forge's own Docker was asked: docker %s", strings.Join(c.Args, " "))
	return nil, docker.ErrNoLocalBackend
}

// A change's preview on a forge with neither a worker for previews nor a
// local Docker is unavailable, never "not running", and asks no Docker.
func TestInTheNoLocalDockerModeAPreviewWithNowhereToRunIsUnavailable(t *testing.T) {
	t.Parallel()
	s := observationService(t, absentDaemon{t: t})
	got := s.ChangeStatus(context.Background(), ids.Nil, idstest.N(7), true)
	if got.State != Unavailable || got.Reason != "this forge has no local Docker" {
		t.Fatalf("preview: %+v", got)
	}
}

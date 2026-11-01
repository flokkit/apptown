package preview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/preview/internal/previewsql"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
	"github.com/flokkit/apptown/stock"
)

// observationService is the preview owner over a daemon, with no worker:
// what a change's preview button reads.
func observationService(t *testing.T, client docker.Client) *Service {
	t.Helper()
	db := testDB(t)
	targets, dir := deployment.NewTargets(db, nil), t.TempDir()
	return New(db, Dependencies{DataDir: dir, Hosts: deployment.NewHosts(targets, nil, deployment.HostSettings{Scope: docker.Scope{Client: client}, DataDir: dir}),
		Environment: deployment.NewEnvironment(deployment.EnvironmentDependencies{ToolVersions: stock.Versions()})})
}

func TestPreviewObservationAndWireShape(t *testing.T) {
	t.Parallel()
	fake := newDockerFake()
	s := observationService(t, fake)
	inProgress := true
	observe := func() Status {
		_ = s.ObservePreviews(t.Context(), []Subject{{ID: idstest.N(7), InProgress: inProgress}}, s.now())
		return s.ChangeStatus(context.Background(), ids.Nil, idstest.N(7), inProgress)
	}
	if got := observe(); got.State != none {
		t.Fatalf("missing current contract: %+v", got)
	}
	inProgress = false
	if got := observe(); got.State != cold {
		t.Fatalf("historical snapshot remains bootable: %+v", got)
	}
	inProgress = true
	dir := filepath.Join(s.dataDir, "workspaces", "change-00000000-0007-7000-8000-000000000000", ".apptown")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("app:\n  web: {image: example/app:1, public: true, port: 8080}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := observe(); got.State != cold {
		t.Fatalf("declared app: %+v", got)
	}
	seedObservedPreview(t, fake, docker.PreviewChangeName(idstest.N(7)))
	got := observe()
	if got.State != live || got.URL != s.env.Routes().PreviewURL("change-00000000-0007-7000-8000-000000000000", 43210) {
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
	if len(wire) != 3 || wire["state"] != string(live) || wire["url"] != got.URL {
		t.Fatalf("wire shape: %s", body)
	}
	boot, err := s.runtime.begin(context.Background(), changeKey(idstest.N(7)))
	if err != nil {
		t.Fatal(err)
	}
	defer boot.finish(nil)
	before := len(fake.Commands())
	if got := s.ChangeStatus(t.Context(), ids.Nil, idstest.N(7), inProgress); got.State != stateStarting || got.At.IsZero() {
		t.Fatalf("active boot: %+v", got)
	}
	if len(fake.Commands()) != before {
		t.Fatal("status queried provider during a boot")
	}
}

// absentDaemon is an AppTown with no Docker of its own; asking it fails the test.
type absentDaemon struct {
	docker.Refusing
	t *testing.T
}

func (d absentDaemon) Run(_ context.Context, _ string, c docker.Command) ([]byte, error) {
	d.t.Errorf("AppTown's own Docker was asked: docker %s", strings.Join(c.Args, " "))
	return nil, docker.ErrNoLocalBackend
}

// A change's preview on an AppTown with neither a worker for previews nor a
// local Docker is unavailable, never "not running", and asks no Docker.
func TestInTheNoLocalDockerModeAPreviewWithNowhereToRunIsUnavailable(t *testing.T) {
	t.Parallel()
	s := observationService(t, absentDaemon{t: t})
	_ = s.ObservePreviews(t.Context(), []Subject{{ID: idstest.N(7), InProgress: true}}, s.now())
	got := s.ChangeStatus(context.Background(), ids.Nil, idstest.N(7), true)
	if got.State != unavailable || !strings.Contains(got.Reason, "this AppTown has no local Docker") {
		t.Fatalf("preview: %+v", got)
	}
}

// A running change preview says when it was built and, only when it was
// booted from a commit, which: a change's working files are not a commit.
// Reusing the same incarnation is not a build.
func TestALivePreviewSaysWhenAndFromWhatItWasBuilt(t *testing.T) {
	t.Parallel()
	fake := newDockerFake()
	s := observationService(t, fake)
	change := idstest.N(8)
	name := docker.PreviewChangeName(change)
	seedObservedPreview(t, fake, name)
	observe := func() Status {
		_ = s.ObservePreviews(t.Context(), []Subject{{ID: change}}, s.now())
		return s.ChangeStatus(context.Background(), ids.Nil, change, false)
	}
	if got := observe(); got.State != live || !got.Built.IsZero() || got.Commit != "" {
		t.Fatalf("a preview AppTown keeps no record of claimed a build: %+v", got)
	}
	first := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	s.recordServedSince(name, s.discardsSoFar(name), servedRecord{incarnation: "folder+1", at: first})
	if got := observe(); !got.Built.Equal(first) || got.Commit != "" {
		t.Fatalf("working files: %+v", got)
	}
	s.recordServedSince(name, s.discardsSoFar(name), servedRecord{incarnation: "folder+1", at: first.Add(time.Hour)})
	if got := observe(); !got.Built.Equal(first) {
		t.Fatalf("a reuse moved the build time: %+v", got)
	}
	s.recordServedSince(name, s.discardsSoFar(name), servedRecord{incarnation: "abc+1", source: "abc", at: first.Add(2 * time.Hour)})
	if got := observe(); !got.Built.Equal(first.Add(2*time.Hour)) || got.Commit != "abc" {
		t.Fatalf("booted from a commit: %+v", got)
	}
}

func TestPreviewPageNeverChecksAndTheReadingSurvivesRestart(t *testing.T) {
	t.Parallel()
	daemon := newDockerFake()
	s := observationService(t, daemon)
	id := idstest.N(7)
	before := len(daemon.Commands())
	if got := s.ChangeStatus(t.Context(), ids.Nil, id, false); got.State != cold {
		t.Fatalf("unobserved preview: %+v", got)
	}
	if _, err := s.LiveChangePreviews(t.Context()); err == nil {
		t.Fatal("unobserved inventory was known")
	}
	if len(daemon.Commands()) != before {
		t.Fatal("cold page contacted Docker")
	}
	seedObservedPreview(t, daemon, docker.PreviewChangeName(id))
	if err := s.ObservePreviews(t.Context(), []Subject{{ID: id}}, s.now()); err != nil {
		t.Fatal(err)
	}
	before = len(daemon.Commands())
	fresh := New(s.db, Dependencies{Hosts: s.hosts, Environment: s.env})
	if got := fresh.ChangeStatus(t.Context(), ids.Nil, id, false); got.State != live || got.Reading.ObservedAt.IsZero() {
		t.Fatalf("restart: %+v", got)
	}
	if live, err := fresh.LiveChangePreviews(t.Context()); err != nil || len(live) != 1 {
		t.Fatalf("stored inventory: %+v %v", live, err)
	}
	if len(daemon.Commands()) != before {
		t.Fatal("warm page contacted Docker")
	}
	changed := New(s.db, Dependencies{Hosts: s.hosts, Environment: s.env, ObservationSource: "different-worker"})
	if got := changed.ChangeStatus(t.Context(), ids.Nil, id, false); !got.Reading.Unknown || got.State != unavailable {
		t.Fatalf("changed preview source inherited evidence: %+v", got)
	}

}

func seedObservedPreview(t *testing.T, client docker.Client, name string) {
	t.Helper()
	if _, err := (docker.Scope{Client: client}).Run(t.Context(), "run", "-d", "--name", name, "fixture-image"); err != nil {
		t.Fatal(err)
	}
}

func TestARecoveredReadingClearsAnUnavailablePreviewBoot(t *testing.T) {
	t.Parallel()
	s := observationService(t, newDockerFake())
	id := idstest.N(7)
	now := time.Now()
	s.clock = func() time.Time { return now }
	s.runtime.now = s.clock
	if err := s.ObservePreviews(t.Context(), []Subject{{ID: id}}, s.now()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	boot, err := s.runtime.begin(t.Context(), changeKey(id))
	if err != nil {
		t.Fatal(err)
	}
	boot.finish(&unavailableError{Cause: errors.New("worker did not answer")})
	if got := s.ChangeStatus(t.Context(), ids.Nil, id, false); got.State != unavailable {
		t.Fatalf("boot outage blamed the app: %+v", got)
	}
	now = now.Add(time.Minute)
	if err := s.ObservePreviews(t.Context(), []Subject{{ID: id}}, s.now()); err != nil {
		t.Fatal(err)
	}
	if got := s.ChangeStatus(t.Context(), ids.Nil, id, false); got.State != cold {
		t.Fatalf("recovered backend still shows its old outage: %+v", got)
	}
}

func TestAnOldNoContractReadingCannotHideANewerFailedBoot(t *testing.T) {
	t.Parallel()
	s := observationService(t, newDockerFake())
	now := time.Now()
	s.clock = func() time.Time { return now }
	s.runtime.now = s.clock
	id := idstest.N(8)
	if err := s.ObservePreviews(t.Context(), []Subject{{ID: id, InProgress: true}}, s.now()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	boot, err := s.runtime.begin(t.Context(), changeKey(id))
	if err != nil {
		t.Fatal(err)
	}
	boot.finish(errors.New("the app build failed"))
	if got := s.ChangeStatus(t.Context(), ids.Nil, id, true); got.State != Broken || got.Reason != "the app build failed" {
		t.Fatalf("old observation hid the newer boot failure: %+v", got)
	}
}

type immediatePreviewChecks struct {
	s       *Service
	project ids.ID
	calls   int
}

func (q *immediatePreviewChecks) RequestPreviewObservation(ctx context.Context, id ids.ID) error {
	q.calls++
	return q.s.ObserveChange(ctx, Subject{ID: id, ProjectID: q.project})
}

func TestPreviewActsRefreshTheirOwnReadingWithoutThePeriodicPass(t *testing.T) {
	t.Parallel()
	daemon := newDockerFake()
	s := observationService(t, daemon)
	id, pid := idstest.N(7), idstest.N(3)
	q := &immediatePreviewChecks{s: s, project: pid}
	s.observationQueue = q
	s.host = previewHostFunc(func(ctx context.Context, _ Change, restart bool) (Result, error) {
		if restart {
			_, _ = (docker.Scope{Client: daemon}).Run(ctx, "rm", "-f", docker.PreviewChangeName(id))
		}
		seedObservedPreview(t, daemon, docker.PreviewChangeName(id))
		return Result{URL: "http://preview"}, nil
	})
	for _, restart := range []bool{false, true} {
		if _, err := s.RehearseChange(t.Context(), Change{ID: id, ProjectID: pid}, restart); err != nil {
			t.Fatal(err)
		}
		got := s.ChangeStatus(t.Context(), pid, id, false)
		if got.State != live || got.URL == "" || got.Reading.ObservedAt.IsZero() {
			t.Fatalf("post-act preview: %+v", got)
		}
	}
	if q.calls != 2 {
		t.Fatalf("start/restart checks: %d", q.calls)
	}
	// The trusted stop participant calls the same queue after removal. A local
	// fixture needs Deployment's preview records, just as production does.
	s.deployments = deployment.NewDeployments(s.db)
	if err := s.DiscardChangePreview(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if got := s.ChangeStatus(t.Context(), pid, id, false); got.State != cold || got.URL != "" || q.calls != 3 {
		t.Fatalf("stopped preview: %+v checks=%d", got, q.calls)
	}
}

func TestPreviewStatusReadsOnlyItsChangeAndPassPrunesOldReadings(t *testing.T) {
	t.Parallel()
	s := observationService(t, newDockerFake())
	first, second := idstest.N(7), idstest.N(8)
	if err := s.ObservePreviews(t.Context(), []Subject{{ID: first}, {ID: second}}, s.now()); err != nil {
		t.Fatal(err)
	}
	// Corrupting a different change cannot affect this change's Try it poll.
	// sql-guarantee: inject a malformed persisted reading that no owner operation can create.
	result, err := s.db.SQL().ExecContext(t.Context(), `UPDATE preview_readings SET payload='"malformed reading"'::jsonb WHERE subject=$1`, second.String())
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("the other change has no independent reading: rows=%d err=%v", n, err)
	}
	if got := s.ChangeStatus(t.Context(), ids.Nil, first, false); got.State != cold || got.Reading.Unknown {
		t.Fatalf("own reading: %+v", got)
	}
	if err := s.ObservePreviews(t.Context(), []Subject{{ID: first}}, s.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := previewsql.New(s.db.SQL()).Reading(t.Context(), second.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old reading retained: %v", err)
	}
}

func TestPreviewPassKeepsAnActionReadingNewerThanItsSelection(t *testing.T) {
	t.Parallel()
	s := observationService(t, newDockerFake())
	started := s.now().Add(-time.Minute)
	id := idstest.N(9)
	if err := s.ObserveChange(t.Context(), Subject{ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := s.ObservePreviews(t.Context(), nil, started); err != nil {
		t.Fatal(err)
	}
	got := s.ChangeStatus(t.Context(), ids.Nil, id, false)
	if got.Reading.Unknown || got.Reading.ObservedAt.IsZero() {
		t.Fatalf("pass pruned a newer action reading: %+v", got)
	}
}

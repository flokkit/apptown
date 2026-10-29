package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/docker/dockerfake"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func healthWorld(t *testing.T) (*Service, *database.DB, *dockerfake.Daemon, ids.ID, *Target) {
	t.Helper()
	db := testDB(t)
	projectID := createTestProject(t, db, "p", "P")
	daemon := dockerfake.New()
	dir := t.TempDir()
	s := New(context.Background(), db, assembled(db, testRuntime{Scope: docker.Scope{Client: daemon}, DataDir: dir, Projects: project.New(db, nil), Secrets: secret.NewService(db, secret.Config{DataDir: dir})}, Dependencies{}))
	target, err := s.Targets.Put(context.Background(), &Target{Name: "local", Kind: KindDockerHost, Origin: OriginBuiltIn, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	return s, db, daemon, projectID, target
}

// A standing deployment whose public part fails liveness gets restarted — but only
// after failureThreshold consecutive misses, and then held by backoff. This is
// the gap docker's exit-based restart policy leaves (a running-but-hung app).
func TestHealthControllerRestartsOnThreshold(t *testing.T) {
	t.Parallel()
	s, db, f, projectID, target := healthWorld(t)
	const container = "apptown-env-p-main"
	closedPort := healthClosedPort(t) // TCP liveness dials this → connection refused → miss
	if _, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.ReserveHostPort(t.Context(), projectID, "main", container, closedPort); err != nil {
		t.Fatal(err)
	}
	f.SetRunning(container, true) // StartedAt is empty, so startup grace has passed.

	hc := s.HealthController()
	at := time.Now()
	hc.Clock(func() time.Time { return at })
	restarts := func() int { return len(f.Matching("restart " + container)) }

	// Below threshold: no restart yet.
	for i := 0; i < docker.HealthFailThreshold-1; i++ {
		at = at.Add(docker.HealthTick)
		hc.Sweep(context.Background())
	}
	if restarts() != 0 {
		t.Fatalf("restarted before %d misses: %d", docker.HealthFailThreshold, restarts())
	}
	// The threshold miss triggers exactly one restart.
	at = at.Add(docker.HealthTick)
	hc.Sweep(context.Background())
	if restarts() != 1 {
		t.Fatalf("want 1 restart at threshold, got %d", restarts())
	}
	// Immediately after, backoff holds it — no second restart on the next sweep.
	hc.Sweep(context.Background())
	if restarts() != 1 {
		t.Fatalf("backoff should hold the restart; got %d", restarts())
	}
	// The restart was recorded to the ledger.
	if n := len(receipts(t, db, "deployment.health-restart")); n < 1 {
		t.Fatalf("health-restart not audited (%d)", n)
	}
}

// A standing deployment that answers liveness is never restarted.
func TestHealthControllerLeavesHealthyEnvAlone(t *testing.T) {
	t.Parallel()
	s, _, f, projectID, target := healthWorld(t)
	// A real TCP listener stands in for a healthy app: the default liveness dials
	// its port and connects.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	const container = "apptown-env-p-main"
	if _, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.ReserveHostPort(t.Context(), projectID, "main", container, port); err != nil {
		t.Fatal(err)
	}
	f.SetRunning(container, true)

	hc := s.HealthController()
	at := time.Now()
	hc.Clock(func() time.Time { return at })
	for i := 0; i < docker.HealthFailThreshold+2; i++ {
		at = at.Add(docker.HealthTick)
		hc.Sweep(context.Background())
	}
	if !hc.Watching(container) {
		t.Fatal("healthy environment was never observed")
	}
	if got := len(f.Matching("restart " + container)); got != 0 {
		t.Fatalf("a healthy env must never be restarted; got %d", got)
	}
}

// A target with missing identity, another provider, or unusable SSH
// credentials never falls back to the local Docker daemon.
func TestHealthControllerNeverUsesLocalDockerForInvalidTarget(t *testing.T) {
	t.Parallel()
	s, db, daemon, projectID, _ := healthWorld(t)
	invalid := []ids.ID{ids.Nil, idstest.N(999999)}
	for _, target := range []*Target{
		{Name: "cloud", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady},
		{Name: "broken-host", Kind: KindDockerHost, Origin: OriginAttached, Status: TargetReady,
			Address: map[string]any{"host": "ssh://root@remote.example", "keyFrom": "MISSING_KEY"}},
	} {
		saved, err := s.Targets.Put(context.Background(), target)
		if err != nil {
			t.Fatal(err)
		}
		invalid = append(invalid, saved.ID)
	}
	for i, targetID := range invalid {
		t.Run(fmt.Sprint(targetID), func(t *testing.T) {
			name := fmt.Sprintf("main-%d", i)
			container := "apptown-env-p-" + name
			if targetID.IsZero() || targetID == idstest.N(999999) {
				// sql-guarantee: Direct INSERT injects an invalid target reference that EnsureStanding refuses, proving fail-closed Docker resolution.
				if _, err := db.SQL().ExecContext(context.Background(), `INSERT INTO deployments (id,project_id,name,container,target_id,updated_at) VALUES ($1,$2,$3,$4,$5,$6)`, ids.New(time.Now()),
					projectID, name, container, targetID, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.Deployments.EnsureStanding(t.Context(), projectID, name, targetID, Attributes{}.WithDefaults()); err != nil {
				t.Fatal(err)
			} else if _, err := s.Deployments.ReserveHostPort(t.Context(), projectID, name, container, 41000+i); err != nil {
				t.Fatal(err)
			}
			daemon.SetRunning(container, true)
			hc := s.HealthController()
			hc.Seed(container, docker.HealthFailThreshold)
			if _, err := s.hosts.DockerScopeFor(context.Background(), targetID); err == nil {
				t.Fatal("invalid target must fail resolution")
			}
			hc.Sweep(context.Background())
			if hc.Watching(container) {
				t.Fatal("unresolved target retained liveness observations")
			}
			if got := daemon.Commands(); len(got) != 0 {
				t.Fatalf("invalid target contacted local Docker: %v", got)
			}
		})
	}
}

func healthClosedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return port
}

// crashLoopWorld is a standing deployment whose container Docker keeps
// restarting: between restarts, so never running when the sweep looks.
const crashLoopContainerID = "crash-loop-container-id"

func crashLoopWorld(t *testing.T) (*Service, *database.DB, *dockerfake.Daemon, *docker.HealthController, string) {
	t.Helper()
	s, db, f, projectID, target := healthWorld(t)
	const container = "apptown-env-p-main"
	if _, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.ReserveHostPort(t.Context(), projectID, "main", container, healthClosedPort(t)); err != nil {
		t.Fatal(err)
	}
	f.SetRunning(container, false)
	dep, err := s.Deployments.ByName(t.Context(), projectID, "main")
	if err != nil {
		t.Fatal(err)
	}
	f.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat {
			out, err := json.Marshal(docker.Runtime{ID: crashLoopContainerID, Labels: deploy.DockerIdentity{Deployment: dep.Label(), Generation: dep.ObservedGeneration}.Labels()})
			return out, err, true
		}
		return nil, nil, false
	})
	return s, db, f, s.HealthController(), container
}

// A container Docker keeps restarting is never up long enough to miss a
// liveness probe, so the controller reads Docker's own restart counter and
// holds the container by stopping it: once, with a receipt that says why.
func TestHealthControllerHoldsACrashLoop(t *testing.T) {
	t.Parallel()
	_, db, f, hc, container := crashLoopWorld(t)
	ctx := context.Background()
	stops := func() int { return len(f.Matching("stop " + crashLoopContainerID)) }
	f.SetRestartCount(container, 2542)
	f.SetExitCode(crashLoopContainerID, 101)

	hc.Sweep(ctx) // first sight opens the window: a high count is history, not a rate
	f.SetRestartCount(container, 2542+docker.CrashLoopRestarts-1)
	hc.Sweep(ctx)
	if stops() != 0 {
		t.Fatalf("held below the threshold: %d stops", stops())
	}
	f.SetRestartCount(container, 2542+docker.CrashLoopRestarts)
	hc.Sweep(ctx)
	if stops() != 1 {
		t.Fatalf("want one stop at the threshold, got %d", stops())
	}
	hc.Sweep(ctx)
	if stops() != 1 {
		t.Fatalf("a held container was stopped again: %d", stops())
	}
	if got := len(f.Matching("restart " + container)); got != 0 {
		t.Fatalf("a crash loop is held, never restarted: %d", got)
	}

	receipts, err := audit.Recent(ctx, db, audit.Filter{KindPrefix: "deployment.crash-loop-held"})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 {
		t.Fatalf("want one hold receipt, got %d", len(receipts))
	}
	var payload struct {
		Restarts, RestartCount int
		DeploymentID           ids.ID
		Generation             int64
		ContainerID            string
		ExitCode               string
		OK                     bool
	}
	if err := json.Unmarshal(receipts[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	dep, err := NewDeployments(db).ByName(ctx, receipts[0].ProjectID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if payload.DeploymentID != dep.ID || payload.Generation != dep.ObservedGeneration || payload.ContainerID != crashLoopContainerID {
		t.Fatalf("hold identity: %+v", payload)
	}
	if payload.Restarts != docker.CrashLoopRestarts || payload.RestartCount != 2542+docker.CrashLoopRestarts || payload.ExitCode != "101" || !payload.OK {
		t.Fatalf("the receipt does not say why: %+v", payload)
	}
}

// Restarts spread over more than the window are not a loop, and a counter
// that drops (Docker resets it on a manual start) reopens the window rather
// than counting as a loop or hiding the next one.
func TestHealthControllerCountsRestartsAsARate(t *testing.T) {
	t.Parallel()
	_, _, f, hc, container := crashLoopWorld(t)
	ctx := context.Background()
	stops := func() int { return len(f.Matching("stop " + crashLoopContainerID)) }
	now := time.Now()
	hc.Clock(func() time.Time { return now })

	f.SetRestartCount(container, 0)
	hc.Sweep(ctx)
	now = now.Add(docker.CrashLoopWindow + time.Minute)
	f.SetRestartCount(container, 4)
	hc.Sweep(ctx) // the window had closed: this opens a new one at 4
	now = now.Add(docker.CrashLoopWindow + time.Minute)
	f.SetRestartCount(container, 8)
	hc.Sweep(ctx) // and again
	if stops() != 0 {
		t.Fatalf("slow restarts were held: %d", stops())
	}
	f.SetRestartCount(container, 1) // a manual docker start reset the counter
	hc.Sweep(ctx)
	if stops() != 0 {
		t.Fatalf("a counter reset was held: %d", stops())
	}
	now = now.Add(time.Minute)
	f.SetRestartCount(container, 1+docker.CrashLoopRestarts)
	hc.Sweep(ctx)
	if stops() != 1 {
		t.Fatalf("a loop after a reset was not held: %d", stops())
	}
}

// A hold cannot stop or attribute a replacement that was not the selected
// generation, nor turn an unreadable container into a witnessed hold.
func TestACrashLoopHoldRequiresTheSelectedContainerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		wrongLabel, wrongGeneration, unreadable bool
	}{
		{name: "different deployment", wrongLabel: true},
		{name: "different generation", wrongGeneration: true},
		{name: "inspection unavailable", unreadable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, db, f, hc, container := crashLoopWorld(t)
			watched := s.hosts.Watched(t.Context(), s.Deployments)
			if len(watched) != 1 {
				t.Fatalf("watched: %+v", watched)
			}
			id := watched[0].Identity
			if tc.wrongLabel {
				id.Deployment = "unrelated"
			}
			if tc.wrongGeneration {
				id.Generation++
			}
			f.SetReply(func(args []string) ([]byte, error, bool) {
				if len(args) == 4 && args[0] == "inspect" && args[2] == docker.RuntimeFormat {
					if tc.unreadable {
						return nil, errors.New("daemon unavailable"), true
					}
					out, err := json.Marshal(docker.Runtime{ID: "replacement-container", Labels: id.Labels()})
					return out, err, true
				}
				return nil, nil, false
			})
			f.SetRestartCount(container, 0)
			hc.Sweep(t.Context())
			f.SetRestartCount(container, docker.CrashLoopRestarts)
			hc.Sweep(t.Context())
			if got := f.Matching("stop"); len(got) != 0 {
				t.Fatalf("unwitnessed container was stopped: %v", got)
			}
			entries, err := audit.Recent(t.Context(), db, audit.Filter{KindPrefix: "deployment.crash-loop-held"})
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("unwitnessed hold attributed: %+v", entries)
			}
		})
	}
}

func TestACrashLoopHoldSurvivesReceiptPersistenceFailure(t *testing.T) {
	t.Parallel()
	_, db, f, hc, container := crashLoopWorld(t)
	f.SetRestartCount(container, 0)
	hc.Sweep(t.Context())
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	f.SetRestartCount(container, docker.CrashLoopRestarts)
	hc.Sweep(t.Context())
	if got := f.Matching("stop " + crashLoopContainerID); len(got) != 1 {
		t.Fatalf("failed receipt prevented witnessed hold: %v", got)
	}
	entries, err := audit.Recent(t.Context(), db, audit.Filter{KindPrefix: "deployment.crash-loop-held"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed receipt claimed persistence: %+v", entries)
	}
}

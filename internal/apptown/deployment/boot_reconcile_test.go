package deployment

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// A host reader's lap asks for a repair of a standing deployment whose
// container its fresh listing lacks, and of nothing else: a stopped
// container is listed and left as found (the health controller or a person
// stopped it), and a deployment meant to be stopped, with an attempt open, or
// acted on since the lap began is left to whatever is acting on it.
//
// Kills: repair at boot only; a stopped container rebuilt; a repair asked
// beside an open attempt or over a newer act.
func TestAHostLapRepairsOnlyWhatIsGone(t *testing.T) {
	t.Parallel()
	s, _, daemon, projectID, target := healthWorld(t)
	const container = "apptown-env-p-main"
	dep, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.ReserveHostPort(t.Context(), projectID, "main", container, 41999); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	s.setClock(func() time.Time { return at })
	repairs := &repairQueue{}
	r := HostReaders{Snapshots: NewHostSnapshots(s.db, s.now), Targets: s.Targets, Hosts: s.hosts, Providers: s.providers, Jobs: repairs}
	lap := func(want int, why string) {
		t.Helper()
		at = at.Add(time.Minute)
		repairs.asked = nil
		if err := r.lap(t.Context(), target.ID); err != nil {
			t.Fatal(err)
		}
		if len(repairs.asked) != want {
			t.Fatalf("%s: repairs asked %v, want %d", why, repairs.asked, want)
		}
	}
	daemon.SetRunning(container, true)
	lap(0, "running")
	if _, err := daemon.Run(t.Context(), "", docker.Command{Args: []string{"stop", container}}); err != nil {
		t.Fatal(err)
	}
	lap(0, "stopped")
	if _, err := daemon.Run(t.Context(), "", docker.Command{Args: []string{"rm", "-f", container}}); err != nil {
		t.Fatal(err)
	}
	lap(1, "gone")

	// An act dated after the lap begins: the listing cannot witness it.
	s.setClock(func() time.Time { return at.Add(time.Hour) })
	if err := s.Deployments.MarkActed(t.Context(), dep.ID); err != nil {
		t.Fatal(err)
	}
	s.setClock(func() time.Time { return at })
	r.Snapshots = NewHostSnapshots(s.db, s.now)
	lap(0, "acted on after the lap began")

	// Meant to be stopped (a person's stop, recorded as runtime control does).
	at = at.Add(2 * time.Hour)
	setRuntime := func(state string) {
		t.Helper()
		if _, err := deploymentsql.New(s.db.SQL()).SetRuntimeState(t.Context(), deploymentsql.SetRuntimeStateParams{ID: dep.ID, State: state, Actor: "alice", At: sql.NullTime{Time: at.Add(-time.Hour), Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	setRuntime("stopped")
	lap(0, "meant to be stopped")
	setRuntime("running")
	lap(1, "meant to run again")

	if _, _, err := s.Outcomes.Admit(t.Context(), Start{DeploymentID: dep.ID, TargetID: target.ID, Key: "open", Inputs: Inputs{Revision: "one"}}); err != nil {
		t.Fatal(err)
	}
	lap(0, "an attempt is open")
}

// repairQueue records the repair jobs a lap asks for.
type repairQueue struct{ asked []string }

func (q *repairQueue) Enqueue(_ context.Context, operation, key string, values map[string]any, _ time.Time) (int64, error) {
	if operation == "rebuild" && values["attempt"] != nil {
		q.asked = append(q.asked, key)
	}
	return 1, nil
}

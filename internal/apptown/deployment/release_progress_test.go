package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
)

// Progress survives a new reader, counts from entry on the owner's clock, and
// repeated image pulls never reset it. Outcome settlement freezes the reading.
func TestReleaseProgressPersistsEachStageAndFreezesAtSettlement(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{true: "failed", false: "live"}[failed], func(t *testing.T) {
			t.Parallel()
			f, dep, id, at := releaseOutcomeFixture(t)
			initial, err := NewReleases(f.db).ByID(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			now := initial.StageEnteredAt.Time
			service := &Service{db: f.db, clock: func() time.Time { return now }}
			read := func() *Release {
				t.Helper()
				store := NewReleases(f.db)
				store.clock = func() time.Time { return now }
				rows, err := store.ForProject(t.Context(), dep.ProjectID, 0)
				if err != nil || len(rows) != 1 {
					t.Fatalf("read: %+v %v", rows, err)
				}
				return rows[0]
			}
			for _, stage := range []deploy.DeployStage{deploy.Preparing, deploy.BackingUp, deploy.Building, deploy.Starting, deploy.Checking} {
				if err := service.recordReleaseStage(t.Context(), id, stage); err != nil {
					t.Fatal(err)
				}
				entered := now
				now = now.Add(15 * time.Second)
				if err := service.recordReleaseStage(t.Context(), id, stage); err != nil {
					t.Fatal(err)
				}
				r := read()
				if r.Stage != stage || !r.StageEnteredAt.Valid || !r.StageEnteredAt.Time.Equal(entered) || r.StageElapsedMs == nil || *r.StageElapsedMs != 15000 {
					t.Fatalf("stage %s: %+v", stage, r)
				}
				now = now.Add(3 * time.Second)
				if r = read(); r.StageElapsedMs == nil || *r.StageElapsedMs != 18000 {
					t.Fatalf("poll didn't advance: %+v", r)
				}
			}
			f.outcomes.clock = func() time.Time { return now }
			err = nil
			if failed {
				_, err = f.outcomes.Fail(t.Context(), at, deploy.Terminal(errors.New("check failed")))
			} else {
				_, err = f.outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", ImageDigest: "selected-image"}})
			}
			if err != nil {
				t.Fatal(err)
			}
			done := read()
			if failed && done.FailedStage != deploy.Checking || !failed && done.Stage != deploy.Live {
				t.Fatalf("settlement lost progress: %+v", done)
			}
			elapsed := *done.StageElapsedMs
			now = now.Add(time.Hour)
			if err := service.recordReleaseStage(t.Context(), id, deploy.Building); err != nil {
				t.Fatal(err)
			}
			if r := read(); r.Stage != done.Stage || *r.StageElapsedMs != elapsed {
				t.Fatalf("terminal progress changed: %+v", r)
			}
		})
	}
}

func TestReleaseProgressWriteFailureIsReturned(t *testing.T) {
	f, _, id, _ := releaseOutcomeFixture(t)
	service := &Service{db: f.db}
	dbtest.FailNextStatement(t, f.db, "-- name: RecordReleaseStage")
	if err := service.recordReleaseStage(context.Background(), id, deploy.Building); err == nil {
		t.Fatal("lost stage write was ignored")
	}
}

// Observe persisted transitions during a real release job, so removing a
// reporter at any execution boundary fails even if the storage helper works.
func TestReleaseJobRecordsEveryStageWithItsEntryTime(t *testing.T) {
	t.Parallel()
	p := newPromotionTargets(t, false)
	p.publish(t)
	selected := p.chooseDefault(t)
	// sql-guarantee: observe owner writes in the isolated database without producing release state.
	_, err := p.db.SQL().ExecContext(t.Context(), `
CREATE TABLE observed_release_stages (stage TEXT, entered_at TIMESTAMPTZ);
CREATE FUNCTION observe_release_stage() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' OR NEW.stage IS DISTINCT FROM OLD.stage THEN
    INSERT INTO observed_release_stages VALUES (NEW.stage, NEW.stage_entered_at);
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER observe_release_stage AFTER INSERT OR UPDATE ON releases
FOR EACH ROW EXECUTE FUNCTION observe_release_stage();`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)
	p.s.clock = func() time.Time { now = now.Add(time.Second); return now }
	p.s.Outcomes.clock = p.s.clock
	p.s.Releases.clock = p.s.clock
	accepted, err := p.s.RequestDefaultRelease(t.Context(), DefaultReleaseSelection{ProjectID: p.project, ExpectedRevision: &selected.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.s.ExecuteRelease(t.Context(), accepted.ID); err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: inspect independently observed persistence, including each transition time.
	rows, err := p.db.SQL().QueryContext(t.Context(), `SELECT stage, entered_at FROM observed_release_stages ORDER BY entered_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stages []string
	var previous time.Time
	for rows.Next() {
		var stage string
		var entered time.Time
		if err := rows.Scan(&stage, &entered); err != nil {
			t.Fatal(err)
		}
		if !entered.After(previous) {
			t.Fatalf("%s entry time %s does not follow %s", stage, entered, previous)
		}
		previous = entered
		stages = append(stages, stage)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stages, ","); got != "preparing,backing-up,building,starting,checking,live" {
		t.Fatalf("release job stages: %s", got)
	}
}

package deployment

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/deploy"
)

func TestReviewInterruptedReleaseDoesNotReportAnActiveStage(t *testing.T) {
	service, _, db, pid, dep, bundle := doReleaseWorld(t)
	accepted := doAcceptRelease(t, service, pid, dep, bundle)
	// Use the real uncertainty path: the provider effect happens but its
	// checkpoint cannot be recorded, so only observation can settle it.
	dbtest.FailNextStatement(t, db, "UPDATE deployment_attempts SET observed_json")
	if err := service.ExecuteRelease(context.Background(), accepted.ID); err == nil || !strings.Contains(err.Error(), "injected statement failure") {
		t.Fatalf("expected checkpoint failure, got %v", err)
	}
	if _, status, err := service.Outcomes.AttemptByKey(t.Context(), dep.ID, fmt.Sprintf("release-%d", accepted.ID)); err != nil || status != AttemptUncertain {
		t.Fatalf("expected an uncertain attempt: %s %v", status, err)
	}
	now := time.Now().Add(time.Hour)
	_, err := database.Transact(context.Background(), db, func(tx *database.Tx) (int64, error) {
		_, durable, err := RepairInterruptedReleasesIn(context.Background(), tx, nil, now)
		return durable, err
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := NewReleases(db)
	reader.clock = func() time.Time { return now }
	r, err := reader.ByID(context.Background(), accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stage != 0 {
		t.Fatalf("no executor is running after restart, but current stage is %s", r.Stage)
	}
	if !r.ProgressUncertain || r.LastStage != deploy.Starting || r.StageElapsedMs != nil {
		t.Fatalf("recovery must retain only historical progress: %+v", r)
	}
}

func TestReleaseProgressRecoveryCompletionRestoresLiveReading(t *testing.T) {
	f, dep, id, at := releaseOutcomeFixture(t)
	useRecoveryJobs(t, f)
	service := &Service{db: f.db}
	if err := service.recordReleaseStage(t.Context(), id, deploy.Checking); err != nil {
		t.Fatal(err)
	}
	// Startup marks admitted execution uncertain before it accepts page reads.
	if _, err := PrepareRecovery(t.Context(), f.outcomes); err != nil {
		t.Fatal(err)
	}
	read := func() *Release {
		t.Helper()
		rows, err := NewReleases(f.db).ForProject(t.Context(), dep.ProjectID, 0)
		if err != nil || len(rows) != 1 {
			t.Fatalf("read: %+v %v", rows, err)
		}
		return rows[0]
	}
	if r := read(); !r.ProgressUncertain || !r.InterruptedByRestart || r.LastStage != deploy.Checking || r.Stage != 0 || r.StageElapsedMs != nil {
		t.Fatalf("interrupted progress: %+v", r)
	}
	if _, err := f.outcomes.Complete(t.Context(), Completion{Attempt: at, Observed: deploy.Observation{Generation: at.Generation, Rollout: "active", ImageDigest: "selected-image"}}); err != nil {
		t.Fatal(err)
	}
	if r := read(); r.ProgressUncertain || r.LastStage != 0 || r.Stage != deploy.Live || r.StageElapsedMs == nil {
		t.Fatalf("recovered progress: %+v", r)
	}
}

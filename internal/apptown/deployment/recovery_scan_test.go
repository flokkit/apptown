package deployment

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/dbtest"
)

func TestDeploymentRecoveryScanFailureDoesNotQueuePartialWork(t *testing.T) {
	t.Parallel()
	f, _, _, at := releaseOutcomeFixture(t)
	useRecoveryJobs(t, f)
	mustExec(t, f.db, `ALTER TABLE deployment_attempts ALTER COLUMN generation TYPE TEXT USING generation::text`)
	// sql-guarantee: Direct UPDATE injects unreadable generation; typed attempts cannot create this corruption needed to test discovery.
	mustExec(t, f.db, `UPDATE deployment_attempts SET generation='invalid-generation' WHERE id=$1`, at.ID)
	if _, err := PrepareRecovery(context.Background(), f.outcomes); err == nil {
		t.Fatal("invalid discovery row was silently skipped")
	}
	wantN(t, "queued jobs", queuedJobs(t, f.db, "", ""), 0)
	// sql-guarantee: the generation column is deliberately corrupted to text, so the owner's typed attempt read cannot scan this row.
	dbtest.Count(t, f.db, 1, `SELECT COUNT(*) FROM deployment_attempts WHERE id=$1 AND status='running'`, at.ID)
}

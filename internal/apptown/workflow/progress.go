package workflow

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/progress"
)

// CaptureProgress detaches a finished feed and records its tail as step
// evidence: a record of what ran, written though the caller stops.
func CaptureProgress(ctx context.Context, db *database.DB, feeds *progress.Feeds, stepID ids.ID, now func() time.Time) {
	feed, ok := feeds.Finish(stepID)
	if !ok {
		return
	}
	// The component returns a detached tail; persistence happens outside its lock.
	body := strings.Join(feed.Lines, "\n")
	if strings.TrimSpace(body) == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	step, err := ReadStepIn(ctx, db, stepID)
	if err != nil {
		return
	}
	name := fmt.Sprintf("step-%d", stepID)
	if _, err := AddArtifactIn(ctx, db, step.RunID, "agent-log", name, "", body, clock.Or(now)()); err != nil {
		log.Printf("background write failed: %v — artifact %s %q on run %d", err, "agent-log", name, step.RunID)
	}
}

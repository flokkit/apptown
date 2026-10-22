package apptown

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/jobs"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/changeconsole"

	"github.com/flokkit/apptown/internal/apptown/conversation"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
)

func narrativeFor(a *App) narrativeBuilder {
	models := a.modelSelection
	sessions, dataDir := a.Console, a.cfg.dataDir

	return narrativeBuilder{
		work:   a.Work,
		models: a.inference,
		resolve: func(ctx context.Context, projectID ids.ID) (string, string) {
			pattern, slot, _ := models.Resolve(ctx, projectID, "fast")
			return pattern, slot
		},
		evidence: func(ctx context.Context, source workflow.NarrativeSource) (narrativeEvidence, error) {
			diff, err := narrativeDiff(ctx, dataDir, source)
			if err != nil || diff.Empty {
				return diff, err
			}
			// Snapshot the session list under its lock; never hold that lock
			// during Git or model work.
			chats := sessions.List(ctx, console.Match{Scope: conversation.Key(ids.Nil, source.Change.ID)})
			diff.Text += changeconsole.ChatReports(chats)
			return diff, nil
		},
	}
}

// narrativeJob is the job kind of a narrative refresh asked for after a chat
// ends, a turn hands back new work, or a branch is imported: keyed
// change/<id>, one attempt, on the default queue.
const narrativeJob = "narrative"

// narratives refreshes a Change's narrative now (Refresh: the Distill
// button and the run follow-ups), or asks the job queue to (Request).
type narratives struct {
	builder narrativeBuilder
	db      *database.DB
	// engine is AppTown acting for itself, which a queued refresh runs as.
	engine func(context.Context) context.Context
}

func (n narratives) Refresh(ctx context.Context, changeID ids.ID) error {
	return n.builder.Refresh(ctx, changeID)
}

// Request queues a refresh of the Change. A request while one is queued or
// running for it adds nothing; the queued one reads the newest evidence.
func (n narratives) Request(ctx context.Context, changeID ids.ID) error {
	return n.db.InTx(ctx, func(tx *database.Tx) error {
		return jobQueue{}.EnqueueIn(ctx, tx, jobs.Request{Operation: narrativeJob, Key: fmt.Sprintf("change/%d", changeID)})
	})
}

// RunJob is the job queue's handler for narrativeJob, refreshing as the
// engine once the act that asked for it was admitted. A refresh that returns
// an error fails the job; nothing retries it. A completed job is not a
// publication receipt (with no fast model nothing is published):
// change.narrative-updated is.
func (n narratives) RunJob(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	rest, _ := strings.CutPrefix(key, "change/")
	changeID, err := ids.Parse(rest)
	if err != nil || changeID.IsZero() || "change/"+changeID.String() != key {
		return nil, jobs.Permanent(fmt.Errorf("invalid narrative key %q", key))
	}
	if err := n.builder.Refresh(n.engine(ctx), changeID); err != nil {
		return nil, err
	}
	return map[string]any{"changeId": changeID}, nil
}

package maintenance

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// --- workspaces ------------------------------------------------------------------
//
// A change's worktree and a run's workspace outlive their work by
// default, and left alone they accumulate for every change ever made.
// Once a change is shipped or
// abandoned, or a run is done, the hub holds the commits, the ledger
// holds the reports, verdicts and the step's terminal, and a preview of a
// shipped change is built from the hub. The one thing only the worktree
// holds is the agent's raw session transcripts (.apptown/sessions/*.jsonl),
// the complete record of every tool call — kept because old sessions are
// something to learn from. The sweep moves them under
// <data>/transcripts/<entry>/ and removes the rest, after a day's grace.
// An agent step's transcript is also kept by digest
// (<data>/transcripts/<sha256>.jsonl, workflow.StoreTranscript) and read by
// its attempt's receipt; the folder's copies moved here are the record of
// everything else (reviews, older runs), not what a continuation resumes.

const workspaceGrace = 24 * time.Hour

// reapWorkspaceDecision says whether one workspaces/ entry is reapable.
// Pure, so it is tested without a filesystem. closedAt is when the change
// closed or the run last moved; zero means unknown, which is never reaped.
func reapWorkspaceDecision(entry string, now time.Time, changeState func(ids.ID) (workflow.ChangeState, time.Time), runStatus func(ids.ID) (workflow.RunState, time.Time)) (bool, string) {
	switch {
	case strings.HasPrefix(entry, "change-"):
		id := parseTrailingID(entry)
		if id.IsZero() {
			return false, ""
		}
		st, closed := changeState(id)
		if !st.Closed() {
			return false, ""
		}
		if closed.IsZero() || now.Sub(closed) < workspaceGrace {
			return false, ""
		}
		return true, "its change " + st.String() + " " + humanAgo(now.Sub(closed))
	case strings.HasPrefix(entry, "run-"):
		id := parseTrailingID(entry)
		if id.IsZero() {
			return false, ""
		}
		st, moved := runStatus(id)
		if st == 0 || st == workflow.RunRunning {
			return false, ""
		}
		if moved.IsZero() || now.Sub(moved) < workspaceGrace {
			return false, ""
		}
		return true, "its run " + st.String() + " " + humanAgo(now.Sub(moved))
	}
	return false, "" // project hubs and anything else stand
}

func humanAgo(d time.Duration) string {
	switch {
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// keepTranscripts moves a workspace's agent session transcripts to
// <data>/transcripts/<entry>/ (merging with any kept earlier) before the
// workspace goes. Returns how many files it kept.
func (r *Reaper) keepTranscripts(entry, ws string) (int, error) {
	src := filepath.Join(ws, ".apptown", "sessions")
	files, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	dst := filepath.Join(r.dataDir, "transcripts", entry)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	kept := 0
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if err := os.Rename(filepath.Join(src, f.Name()), filepath.Join(dst, f.Name())); err != nil {
			return kept, err
		}
		kept++
	}
	return kept, nil
}

// A saved conversation owns its checkout even when its container is idle or
// absent. Ending the conversation removes it from this registry and releases
// that ownership. New Change chats only attach to in-progress Changes, which
// are independently ineligible for workspace reaping.

// reapWorkspaces sweeps <data>/workspaces: the worktree of a change that
// shipped or was abandoned, and the workspace of a run that finished,
// after the grace period, keeping the transcripts first. Saved conversations
// retain ownership until explicitly ended.
func (r *Reaper) Workspaces(ctx context.Context, logOnly bool) []string {
	root := filepath.Join(r.dataDir, "workspaces")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	now := r.now()
	changeState := func(id ids.ID) (workflow.ChangeState, time.Time) {
		c, err := workflow.ReadChangeIn(ctx, r.db, id)
		if err != nil || c == nil {
			return 0, time.Time{}
		}
		if !c.ClosedAt.Valid {
			return c.State, time.Time{}
		}
		return c.State, c.ClosedAt.Time
	}
	runStatus := func(id ids.ID) (workflow.RunState, time.Time) {
		r, err := workflow.ReadRunIn(ctx, r.db, id)
		if err != nil || r == nil {
			return 0, time.Time{}
		}
		t, _ := time.Parse(time.RFC3339, r.UpdatedAt)
		return r.Status, t
	}
	var reaped []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ws := filepath.Join(root, e.Name())
		if r.sessions.UsesWorkspace(ctx, ws) {
			continue
		}
		ok, why := reapWorkspaceDecision(e.Name(), now, changeState, runStatus)
		if !ok {
			continue
		}
		reaped = append(reaped, e.Name())
		if logOnly {
			log.Printf("reaper: WOULD remove workspace %s (%s)", e.Name(), why)
			continue
		}
		kept, err := r.keepTranscripts(e.Name(), ws)
		if err != nil {
			log.Printf("reaper: retaining workspace %s: preserving transcripts: %v", e.Name(), err)
			continue
		}
		log.Printf("reaper: removing workspace %s (%s); %d transcript file(s) kept under transcripts/%s", e.Name(), why, kept, e.Name())
		if err := os.RemoveAll(ws); err != nil {
			log.Printf("reaper: workspace %s not removed: %v", e.Name(), err)
		} else if err := scm.ClearSaved(ws); err != nil {
			log.Printf("reaper: the saved record of workspace %s not removed: %v", e.Name(), err)
		}
	}
	return reaped
}

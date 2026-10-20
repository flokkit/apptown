package workflowexec

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	piharness "flokkit.io/forge/internal/forge/agentharness/pi"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// Session history on AppTown's own path follows the same rule as on a
// worker (design/operations/workers.md, "Agent and review steps on the
// worker"): after each invocation the transcript naming the admitted session
// is kept by digest and recorded with the attempt, and a continuing attempt
// resumes exactly the bytes its receipt names. Runs from before transcripts were kept have their
// folder's file imported once, at startup (BackfillTranscripts), so no path
// reads a folder's file instead.

func sessionDir(workspace string) string { return filepath.Join(workspace, ".apptown", "sessions") }

// earlierHistory is the transcript a continuing session resumes: the bytes
// its earlier attempt's receipt names, checked against their digest. A new
// session has none (nil). An earlier attempt that recorded no history, or
// whose history cannot be given back, refuses the attempt (a
// *workflow.HistoryUnavailable): it is never run as a fresh session with
// only the feedback.
func (a *Executor) earlierHistory(ctx context.Context, run *workflow.Run, step *workflow.Step, session string) ([]byte, error) {
	earlier, err := workflow.ContinuedTranscriptIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID}, step.ID, step.Attempt, session)
	if err != nil {
		return nil, &workflow.HistoryUnavailable{Session: session, Reason: "its receipt could not be read: " + err.Error()}
	}
	if earlier.StepID.IsZero() {
		return nil, nil
	}
	return workflow.ReadTranscript(a.cfg.DataDir, earlier)
}

// resumeHistory gives an attempt continuing session the history its earlier
// attempt's receipt kept (earlierHistory), written as the session's one file
// in the folder so Pi appends to those bytes.
func (a *Executor) resumeHistory(ctx context.Context, run *workflow.Run, step *workflow.Step, session, workspace string) error {
	raw, err := a.earlierHistory(ctx, run, step, session)
	if err != nil || raw == nil {
		return err
	}
	if err := piharness.ResumeTranscript(sessionDir(workspace), session, raw, a.now()); err != nil {
		return &workflow.HistoryUnavailable{Session: session, Reason: "it could not be given to the agent: " + err.Error()}
	}
	return nil
}

// keepTranscript captures the session's transcript after one invocation of
// an attempt on AppTown's own path (keepCapture).
func (a *Executor) keepTranscript(ctx context.Context, c piCall, invocation int) error {
	raw, reason := piharness.StepTranscript(sessionDir(c.Workspace), c.Session)
	return a.keepCapture(ctx, workflow.TranscriptCapture{
		Run:    workflow.RunRef{ID: c.Source.RunID, ProjectID: c.Source.ProjectID},
		StepID: c.Step.ID, Attempt: c.Step.Attempt, Session: c.Session, Invocation: int64(invocation),
	}, raw, reason)
}

// keepCapture keeps one invocation's history through the owner's one
// store-and-record operation (workflow.KeepTranscript), so the orphan sweep
// never deletes bytes between their store and their receipt. History that
// cannot be kept (reason, or a failed store) is recorded as unavailable,
// with why; that does not fail the step, since the history is not the step's
// work. The record itself is part of settling the invocation: it is tried
// again (workflow.Settlement), and an error means nothing later may run in the
// session and the attempt may not be settled as if it had been recorded.
func (a *Executor) keepCapture(ctx context.Context, c workflow.TranscriptCapture, raw []byte, reason string) error {
	c.At = a.now()
	// The history of a session that ran is kept though the caller stops:
	// its tries are bounded by the Settlement.
	ctx = context.WithoutCancel(ctx)
	if err := a.Settle.Retry(ctx, func() error {
		return workflow.KeepTranscript(ctx, a.db, a.cfg.DataDir, c, raw, reason)
	}); err != nil {
		return fmt.Errorf("recording the session's history for step %d attempt %d: %w", c.StepID, c.Attempt, err)
	}
	return nil
}

// maxBackfill bounds how many attempts one startup imports; the rest are
// imported at the next.
const maxBackfill = 10000

// BackfillTranscripts imports, once, the history of agent attempts that
// recorded none (runs from before transcripts were kept, and attempts the
// AppTown lost mid-invocation), so every attempt continues through its
// receipt. The history is the one file in the run's folder whose header
// names the session (piharness.StepTranscript: regular files only, never
// the newest file); it is stored and recorded as the attempt's first
// invocation. An attempt whose session a later attempt continued, or whose
// file is not there or cannot be kept, is recorded unavailable, with why.
// Each attempt is recorded once, so running it again imports nothing twice.
// It runs at startup before recorded work resumes.
func (a *Executor) BackfillTranscripts(ctx context.Context) {
	pending, err := workflow.UnrecordedSessionsIn(ctx, a.db, maxBackfill)
	if err != nil {
		log.Printf("startup repair: reading attempts with no recorded history: %v", err)
		return
	}
	read, unavailable, failed := 0, 0, 0
	sources := map[ids.ID]workflow.Source{}
	for _, p := range pending {
		c := workflow.TranscriptCapture{Run: p.Run, StepID: p.StepID, Attempt: p.Attempt, Session: p.Session, Invocation: 1}
		var raw []byte
		reason := "a later attempt continued its session, so its own history was not kept"
		if !p.Superseded {
			source, found := sources[p.Run.ID]
			if !found {
				if source, err = workflow.ReadSourceIn(ctx, a.db, p.Run); err != nil {
					log.Printf("startup repair: reading run %d's folder: %v", p.Run.ID, err)
					failed++
					continue
				}
				sources[p.Run.ID] = source
			}
			raw, reason = piharness.StepTranscript(sessionDir(source.Workspace(a.cfg.DataDir)), p.Session)
			if reason != "" {
				reason = "it ran before transcripts were kept, and its folder's history could not be: " + reason
			}
		}
		if err := a.keepCapture(ctx, c, raw, reason); err != nil {
			log.Printf("startup repair: %v", err)
			failed++
			continue
		}
		if raw != nil {
			read++
		} else {
			unavailable++
		}
	}
	if len(pending) > 0 {
		log.Printf("startup repair: agent histories read from folders=%d recorded unavailable=%d failed=%d (of %d, at most %d a start)", read, unavailable, failed, len(pending), maxBackfill)
	}
}

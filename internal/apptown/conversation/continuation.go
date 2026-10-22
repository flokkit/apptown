package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/strs"
)

// laneContinuation is the server half of "take it to chat" (design/
// change-lifecycle.md): the lane's run stops, the stage's producing agent's
// pi session is copied into the console so the chat RESUMES it — the same
// agent, its memory intact, a person now steering — and the briefing says
// where the lane stopped and what it recorded. The session resumed is the
// one the producer's receipt kept (a run from before transcripts were kept
// had its folder's file imported at startup). When it cannot be given the
// chat starts a fresh helper and the briefing says so, and why.
type laneContinuation struct {
	console.Resumption
	labels map[string]string // the run, step and attempt the resumed agent's inference is charged to
	steer  string
}

// continuationWork is the two-operation handoff protocol; session mechanics do
// not receive a Workflow service or settle work themselves.
type continuationWork interface {
	ChatContinuation(context.Context, ids.ID, ids.ID) (workflow.ChatContinuation, error)
	Abandon(context.Context, workflow.StopWork) (*workflow.Abandoned, error)
}

func continueLane(dataDir string, ctx context.Context, work continuationWork, change *workflow.Change, stepID ids.ID, who string) (*laneContinuation, error) {
	observed, err := work.ChatContinuation(ctx, change.ID, stepID)
	if err != nil {
		return nil, err
	}
	step, run := observed.Step, observed.Run
	c := &laneContinuation{labels: map[string]string{RunLabel: run.ID.String()}}
	historyGone := ""
	if observed.Producer != nil {
		agentName := observed.Producer.Name
		label := fmt.Sprintf("the %s lane's agent (%s)", run.WorkflowName, agentName)
		if history := observed.History; history.Recorded {
			// The chat resumes exactly the bytes the producer's latest
			// attempt kept, checked against its receipt, never the
			// folder's newest file.
			raw, err := workflow.ReadTranscript(dataDir, history)
			if err != nil {
				historyGone = err.Error()
			} else {
				c.Record, c.Session, c.Label = raw, history.Session, label
				c.labels[StepLabel], c.labels[AttemptLabel] = history.StepID.String(), strconv.FormatInt(history.Attempt, 10)
			}
		}
	}
	var ev map[string]any
	_ = json.Unmarshal(step.Evidence, &ev)
	var detail []string
	for _, k := range []string{"reason", "summary"} {
		if v, ok := ev[k].(string); ok && strings.TrimSpace(v) != "" {
			detail = append(detail, strs.Truncate(v, 1500))
		}
	}
	if fs, ok := ev["findings"].([]any); ok {
		for i, f := range fs {
			if i >= 8 {
				break
			}
			if fst, ok := f.(string); ok {
				detail = append(detail, "- "+strs.Truncate(fst, 400))
			}
		}
	}
	if run.Status == workflow.RunRunning {
		if _, err := work.Abandon(ctx, workflow.StopWork{RunID: run.ID, Reason: "taken to chat"}); err != nil {
			return nil, err
		}
	}
	var b strings.Builder
	b.WriteString("\n\n# Where the lane stopped\n")
	fmt.Fprintf(&b, "The %s lane (run %d) stopped in stage %q at step %q (%s, status %s). A person chose to continue in this chat; the lane's run is stopped.\n",
		run.WorkflowName, run.ID, step.Stage, step.Name, step.Type, step.Status)
	if len(detail) > 0 {
		b.WriteString("What it recorded:\n" + strings.Join(detail, "\n") + "\n")
	}
	if c.Record != nil {
		b.WriteString("This conversation CONTINUES your own session from that lane: the messages before this briefing are yours and the commits on the branch are yours. Do not redo work that is done. Start by saying, in two or three plain sentences, where things stand and what you think went wrong; then ask what they want.\n")
	} else if historyGone != "" {
		b.WriteString("The lane's agent session cannot be continued (" + historyGone + "), so you start fresh: read the branch first (git log, git diff against main, the report under .apptown/) before saying anything, then say where things stand in plain words.\n")
	} else {
		b.WriteString("The lane's agent session could not be found, so you start fresh: read the branch first (git log, git diff against main, the report under .apptown/) before saying anything, then say where things stand in plain words.\n")
	}
	c.steer = b.String()
	return c, nil
}

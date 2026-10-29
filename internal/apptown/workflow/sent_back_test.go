package workflow

import (
	"encoding/json"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func sendbackEvent(n uint64, step ids.ID, by, note string) Event {
	payload, _ := json.Marshal(map[string]any{"stepId": step, "by": by, "note": note})
	return Event{ID: idstest.N(n), Kind: "step.sendback", Payload: payload}
}

func agentStep(n uint64, stage, words string) *Step {
	config, _ := json.Marshal(map[string]any{"sendback": words})
	if words == "" {
		config = []byte(`{}`)
	}
	return &Step{ID: idstest.N(n), Stage: stage, Name: "apply-change", Type: "agent/pi", Status: StatusRunning, Config: config}
}

// The run says who sent its current stage's agent back, once for the CLI and
// the web: a person's send-back with no failed verdict is a rework; a stage
// whose agent carries no words is none, whatever an earlier stage was sent
// back with; of two send-backs with the same words, the newer sender is
// named; a check's report and a rejection name theirs.
func TestARunSaysWhoSentItsCurrentAgentBack(t *testing.T) {
	t.Parallel()
	run := &Run{Status: RunRunning, CurrentStage: "build"}
	words := "use crypto/rand"

	agent := agentStep(10, "build", words)
	got := sentBackOf(run, []*Step{agent}, []Event{sendbackEvent(1, agent.ID, "bls", words)})
	if got == nil || got.By != "bls" || got.Note != words || got.StepID != agent.ID || got.Step != "apply-change" {
		t.Fatalf("a person's send-back: %+v", got)
	}

	// Events arrive newest first on a detail read; the newer sender wins either way.
	newer := []Event{sendbackEvent(3, agent.ID, "alice", words), sendbackEvent(2, agent.ID, "bls", words)}
	for _, events := range [][]Event{newer, {newer[1], newer[0]}} {
		if got := sentBackOf(run, []*Step{agent}, events); got == nil || got.By != "alice" {
			t.Fatalf("two send-backs with the same words: %+v", got)
		}
	}

	// A send-back of an earlier stage is not this stage's.
	second := agentStep(20, "test", "")
	run.CurrentStage = "test"
	if got := sentBackOf(run, []*Step{agentStep(10, "build", words), second}, []Event{sendbackEvent(1, idstest.N(10), "bls", words)}); got != nil {
		t.Fatalf("an agent with no words after an earlier stage's send-back: %+v", got)
	}

	// No receipt for these words and no rejection: a check's report, else a person.
	run.CurrentStage = "build"
	if got := sentBackOf(run, []*Step{agentStep(30, "build", checkReportPrefix+":\nfailed")}, nil); got == nil || got.By != "a check" {
		t.Fatalf("a check's report: %+v", got)
	}
	if got := sentBackOf(run, []*Step{agentStep(31, "build", words)}, nil); got == nil || got.By != "a person" {
		t.Fatalf("words with no receipt: %+v", got)
	}
	approval := &Step{ID: idstest.N(40), Stage: "build", Type: "approval", Evidence: []byte(`{"decisions":[],"lastRejection":{"approver":"carol","comment":"no","at":"x"}}`)}
	if got := sentBackOf(run, []*Step{agentStep(32, "build", words), approval}, nil); got == nil || got.By != "carol" {
		t.Fatalf("a rejection: %+v", got)
	}

	// Not running: no rework.
	run.Status = RunDone
	if got := sentBackOf(run, []*Step{agentStep(33, "build", words)}, nil); got != nil {
		t.Fatalf("a finished run: %+v", got)
	}
}

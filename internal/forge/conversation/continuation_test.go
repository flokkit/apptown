package conversation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
)

type continuationScenario struct {
	observation         workflow.ChatContinuation
	observeErr, stopErr error
	stops               []workflow.StopWork
}

func (s *continuationScenario) ChatContinuation(context.Context, ids.ID, ids.ID) (workflow.ChatContinuation, error) {
	return s.observation, s.observeErr
}
func (s *continuationScenario) Abandon(_ context.Context, in workflow.StopWork) (*workflow.Abandoned, error) {
	s.stops = append(s.stops, in)
	return nil, s.stopErr
}

// Workflow's persistence tests protect abandonment rollback. The conversation
// owner must propagate that refusal without promising a chat handoff.
func TestContinuationRefusesFailedAbandonment(t *testing.T) {
	t.Parallel()
	denied := errors.New("abandonment receipt failed")
	s := &continuationScenario{observation: workflow.ChatContinuation{StepContext: workflow.StepContext{Run: &workflow.Run{ID: idstest.N(7), Status: workflow.RunRunning}, Step: &workflow.Step{ID: idstest.N(9)}}}, stopErr: denied}
	got, err := continueLane(t.TempDir(), context.Background(), s, &workflow.Change{ID: idstest.N(3)}, idstest.N(9), "alice")
	if !errors.Is(err, denied) || got != nil {
		t.Fatalf("failed abandonment offered handoff: %+v %v", got, err)
	}
	if len(s.stops) != 1 || s.stops[0].RunID != idstest.N(7) {
		t.Fatalf("handoff must stop the observed run: %+v", s.stops)
	}
}
func TestContinuationKeepsCompletedWorkAndDescribesMissingSession(t *testing.T) {
	t.Parallel()
	s := &continuationScenario{observation: workflow.ChatContinuation{StepContext: workflow.StepContext{Run: &workflow.Run{ID: idstest.N(7), Status: workflow.RunDone}, Step: &workflow.Step{ID: idstest.N(9), Evidence: []byte(`{"summary":"needs another look"}`)}}}}
	got, err := continueLane(t.TempDir(), context.Background(), s, &workflow.Change{ID: idstest.N(3)}, idstest.N(9), "alice")
	if err != nil || len(s.stops) != 0 {
		t.Fatalf("completed history must not be abandoned: %+v %v", s.stops, err)
	}
	if got.Record != nil || !strings.Contains(got.steer, "start fresh") || !strings.Contains(got.steer, "needs another look") || got.labels[RunLabel] != idstest.N(7).String() {
		t.Fatalf("wrong handoff: %+v", got)
	}
}

// laneWithHistory is a stopped lane whose producer, step 4, ran session
// step-4-a1 in the folder dataDir/workspaces/run-7.
func laneWithHistory(t *testing.T, dataDir string, history workflow.TranscriptReceipt) *continuationScenario {
	t.Helper()
	folder := filepath.Join(dataDir, "workspaces", "run-7", ".forge", "sessions")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	// A newer file in the folder names the same step: the kept history, not
	// this, is what the chat resumes.
	if err := os.WriteFile(filepath.Join(folder, "2099-01-01_step-4-a1.jsonl"), []byte(laneTranscript("the folder's newer file")), 0o644); err != nil {
		t.Fatal(err)
	}
	return &continuationScenario{observation: workflow.ChatContinuation{
		StepContext: workflow.StepContext{Run: &workflow.Run{ID: idstest.N(7), Status: workflow.RunDone, WorkflowName: "build"}, Step: &workflow.Step{ID: idstest.N(4), Name: "implement"}},
		Producer:    &workflow.Step{ID: idstest.N(4), Name: "implement"},
		Source:      workflow.Source{RunID: idstest.N(7), WorkspaceRunID: idstest.N(7)},
		History:     history,
	}}
}

func laneTranscript(turn string) string {
	return `{"type":"session","version":3,"id":"step-4-a1","timestamp":"2026-10-01T00:00:00.000Z","cwd":"/work"}` + "\n" +
		`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"` + turn + `"}]}}` + "\n"
}

// Taking a lane to chat resumes the bytes the producer's receipt kept,
// checked against their digest, and ignores a newer file in the folder.
func TestContinuationResumesTheKeptTranscriptNotTheNewestFile(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	kept := []byte(laneTranscript("what the receipt kept"))
	digest, err := workflow.StoreTranscript(dataDir, kept)
	if err != nil {
		t.Fatal(err)
	}
	s := laneWithHistory(t, dataDir, workflow.TranscriptReceipt{StepID: idstest.N(4), Attempt: 2, Session: "step-4-a1", Recorded: true, Digest: digest})
	got, err := continueLane(dataDir, context.Background(), s, &workflow.Change{ID: idstest.N(3)}, idstest.N(4), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Record) != string(kept) || got.Session != "step-4-a1" || !strings.Contains(got.steer, "CONTINUES your own session") {
		t.Fatalf("resumed %q as %q: %s", got.Record, got.Session, got.steer)
	}
	if got.labels[StepLabel] != idstest.N(4).String() || got.labels[AttemptLabel] != "2" {
		t.Fatalf("inference charged to %v", got.labels)
	}
}

// Kept history that cannot be given back starts the chat fresh and says why;
// it is never swapped for the folder's file. Nor is a producer that recorded
// no history (its folder's file was imported at startup if it had one).
func TestContinuationWithUnavailableHistoryStartsFreshAndSaysWhy(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		history workflow.TranscriptReceipt
		want    string
	}{
		"unavailable": {workflow.TranscriptReceipt{StepID: idstest.N(4), Attempt: 1, Session: "step-4-a1", Recorded: true, Unavailable: "no transcript names session"}, "no transcript names session"},
		"missing":     {workflow.TranscriptReceipt{StepID: idstest.N(4), Attempt: 1, Session: "step-4-a1", Recorded: true, Digest: strings.Repeat("ab", 32)}, "missing"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			got, err := continueLane(dataDir, context.Background(), laneWithHistory(t, dataDir, c.history), &workflow.Change{ID: idstest.N(3)}, idstest.N(4), "alice")
			if err != nil {
				t.Fatal(err)
			}
			if got.Record != nil || !strings.Contains(got.steer, "cannot be continued") || !strings.Contains(got.steer, c.want) {
				t.Fatalf("%s: %q %s", name, got.Record, got.steer)
			}
		})
	}
	dataDir := t.TempDir()
	got, err := continueLane(dataDir, context.Background(), laneWithHistory(t, dataDir, workflow.TranscriptReceipt{StepID: idstest.N(4), Attempt: 1, Session: "step-4-a1"}), &workflow.Change{ID: idstest.N(3)}, idstest.N(4), "alice")
	if err != nil || got.Record != nil || !strings.Contains(got.steer, "start fresh") {
		t.Fatalf("a producer with no recorded history: %+v %v", got, err)
	}
}

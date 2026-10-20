package workflowexec

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
)

// piKeepsSession stands in for Pi's session file: it appends one turn to the
// file in the step's session directory whose header names the invocation's
// session, creating the file (header first) when none does, as Pi does. It
// returns what that file held before the turn.
func piKeepsSession(t *testing.T, req execution.Request, text string) string {
	t.Helper()
	session := sessionIDOf(req)
	dir := filepath.Join(req.Workspace, ".apptown", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := ""
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	for _, m := range matches {
		f, err := os.Open(m)
		if err != nil {
			t.Fatal(err)
		}
		first, _ := bufio.NewReader(f).ReadString('\n')
		f.Close()
		var h struct{ Type, ID string }
		if json.Unmarshal([]byte(first), &h) == nil && h.Type == "session" && h.ID == session {
			file = m
		}
	}
	before := ""
	if file == "" {
		file = filepath.Join(dir, fmt.Sprintf("2026-10-01T00-00-%02d-000Z_%s.jsonl", len(matches), session))
		before = fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":"2026-10-01T00:00:00.000Z","cwd":"/work"}`+"\n", session)
		if err := os.WriteFile(file, []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		before = string(raw)
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n", text); err != nil {
		t.Fatal(err)
	}
	return before
}

// keptTranscript reads the bytes an attempt's receipt names.
func keptTranscript(t *testing.T, a *taskForge, runID ids.ID, stepID ids.ID, attempt int64) (string, error) {
	t.Helper()
	run, err := workflow.ReadRunIn(context.Background(), a.db, runID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := workflow.StepTranscriptIn(context.Background(), a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID}, stepID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := workflow.ReadTranscript(a.dir, receipt)
	return string(raw), err
}

// Two consecutive continue retries each resume exactly the bytes the attempt
// before them kept, not what the folder holds by then, and every attempt's
// receipt still reads its own bytes afterwards.
func TestContinueRetriesResumeTheKeptTranscript(t *testing.T) {
	t.Parallel()
	a, runID, stepID := admittedAgentStep(t, `{"prompt":"do it"}`)
	seen := map[int]string{}
	f := installFakePi(a, func(n int, req execution.Request) execution.Outcome {
		seen[n] = piKeepsSession(t, req, fmt.Sprintf("turn %d", n))
		return execution.Outcome{Output: piSays("done")}
	})
	a.work.ExecuteStep(a.ctx, stepID)
	first, err := keptTranscript(t, a, runID, stepID, 1)
	if err != nil || !strings.Contains(first, "turn 1") {
		t.Fatalf("the first attempt kept %q: %v", first, err)
	}
	for attempt := 2; attempt <= 3; attempt++ {
		// Something writes the folder's file after it was kept; a
		// continuation resumes the kept bytes, not these.
		files, _ := filepath.Glob(filepath.Join(f.requests()[0].Workspace, ".apptown", "sessions", "*.jsonl"))
		for _, file := range files {
			if err := os.WriteFile(file, []byte(first+`{"type":"message","note":"never kept"}`+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID, Mode: workflow.RetryContinue, Feedback: "again"}); err != nil {
			t.Fatal(err)
		}
		a.work.ExecuteStep(a.ctx, stepID)
		previous, err := keptTranscript(t, a, runID, stepID, int64(attempt-1))
		if err != nil || seen[attempt] != previous {
			t.Fatalf("attempt %d resumed %q, want attempt %d's kept %q (%v)", attempt, seen[attempt], attempt-1, previous, err)
		}
		if status, ev := stepStatus(t, a, stepID); status != workflow.StatusPass {
			t.Fatalf("attempt %d: %s %v", attempt, status, ev)
		}
		first, _ = keptTranscript(t, a, runID, stepID, int64(attempt))
	}
	for attempt, want := range map[int64][]string{1: {"turn 1"}, 2: {"turn 1", "turn 2"}, 3: {"turn 1", "turn 2", "turn 3"}} {
		got, err := keptTranscript(t, a, runID, stepID, attempt)
		for _, turn := range want {
			if err != nil || !strings.Contains(got, turn) || strings.Contains(got, "never kept") || strings.Contains(got, fmt.Sprintf("turn %d", len(want)+1)) {
				t.Fatalf("attempt %d reads %q (%v)", attempt, got, err)
			}
		}
	}
}

// A continuation whose kept history cannot be given back is refused, with
// that reason, and Pi is not run: it is never a fresh session given only the
// feedback.
func TestAContinuationWithoutItsHistoryIsRefused(t *testing.T) {
	t.Parallel()
	for _, damage := range []string{"never written", "removed", "changed"} {
		t.Run(damage, func(t *testing.T) {
			t.Parallel()
			a, runID, stepID := admittedAgentStep(t, `{"prompt":"do it"}`)
			f := installFakePi(a, func(n int, req execution.Request) execution.Outcome {
				if damage != "never written" {
					piKeepsSession(t, req, "turn")
				}
				return execution.Outcome{Output: piSays("done")}
			})
			a.work.ExecuteStep(a.ctx, stepID)
			want := "no transcript names session"
			if damage != "never written" {
				files, _ := filepath.Glob(filepath.Join(a.dir, "transcripts", "*.jsonl"))
				if len(files) != 1 {
					t.Fatalf("kept files: %v", files)
				}
				if damage == "removed" {
					want = "missing"
					if err := os.Remove(files[0]); err != nil {
						t.Fatal(err)
					}
				} else {
					want = "does not match"
					if err := os.WriteFile(files[0], []byte("changed"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := keptTranscript(t, a, runID, stepID, 1); err == nil {
				t.Fatal("damaged history read back")
			}
			if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID, Mode: workflow.RetryContinue, Feedback: "fix it"}); err != nil {
				t.Fatal(err)
			}
			a.work.ExecuteStep(a.ctx, stepID)
			status, ev := stepStatus(t, a, stepID)
			reason := fmt.Sprint(ev["reason"])
			if status != workflow.StatusError || !strings.HasPrefix(reason, "the earlier session's history is not available") || !strings.Contains(reason, want) {
				t.Fatalf("%s: %s %q", damage, status, reason)
			}
			if n := len(f.requests()); n != 1 {
				t.Fatalf("pi ran %d times; the refused continuation must not run", n)
			}
		})
	}
}

// A run from before transcripts were kept (its start receipt and its
// folder's file, no record) has that file imported at startup, once, and
// then continues through its receipt, never through the folder. One whose
// folder holds no such file is recorded unavailable, and its continuation
// is refused with why.
func TestAnOldRunsFolderHistoryIsImportedAtStartupThenContinued(t *testing.T) {
	t.Parallel()
	for _, folder := range []string{"kept", "gone"} {
		t.Run(folder, func(t *testing.T) {
			t.Parallel()
			a, runID, stepID := admittedAgentStep(t, `{"prompt":"do it"}`)
			seen := map[int]string{}
			f := installFakePi(a, func(n int, req execution.Request) execution.Outcome {
				seen[n] = piKeepsSession(t, req, fmt.Sprintf("turn %d", n))
				return execution.Outcome{Output: piSays("done")}
			})
			a.work.ExecuteStep(a.ctx, stepID)
			first, err := keptTranscript(t, a, runID, stepID, 1)
			if err != nil {
				t.Fatal(err)
			}
			// sql-guarantee: A run from before agent_transcripts has a start receipt and no record; no operation removes a record, so it is deleted to construct that era.
			if _, err := a.db.Exec("DELETE FROM agent_transcripts WHERE step_id=$1", stepID); err != nil {
				t.Fatal(err)
			}
			if folder == "gone" {
				if err := os.RemoveAll(filepath.Join(f.requests()[0].Workspace, ".apptown", "sessions")); err != nil {
					t.Fatal(err)
				}
			}
			a.agents.BackfillTranscripts(context.Background())
			a.agents.BackfillTranscripts(context.Background()) // again: nothing twice
			imported, err := keptTranscript(t, a, runID, stepID, 1)
			if folder == "kept" && (err != nil || imported != first) {
				t.Fatalf("the import kept %q, want the folder's %q (%v)", imported, first, err)
			}
			if folder == "gone" && (err == nil || !strings.Contains(err.Error(), "ran before transcripts were kept")) {
				t.Fatalf("a missing folder file: %q %v", imported, err)
			}
			if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID, Mode: workflow.RetryContinue, Feedback: "again"}); err != nil {
				t.Fatal(err)
			}
			a.work.ExecuteStep(a.ctx, stepID)
			status, ev := stepStatus(t, a, stepID)
			switch {
			case folder == "kept" && (status != workflow.StatusPass || seen[2] != first):
				t.Fatalf("the continuation: %s %v, resumed %q", status, ev, seen[2])
			case folder == "gone" && (status != workflow.StatusError || !strings.Contains(fmt.Sprint(ev["reason"]), "ran before transcripts were kept") || len(f.requests()) != 1):
				t.Fatalf("the continuation of unrecorded history: %s %v", status, ev)
			}
		})
	}
}

// Recording an invocation's history on AppTown's own path is part of
// settling the step: a receipt that cannot be written (after its tries) is
// the step's error, never an attempt later read as one that kept nothing.
func TestAHistoryReceiptThatCannotBeWrittenIsTheStepsError(t *testing.T) {
	t.Parallel()
	a, _, stepID := admittedAgentStep(t, `{"prompt":"do it"}`)
	f := installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
		piKeepsSession(t, req, "turn")
		return execution.Outcome{Output: piSays("")}
	})
	a.agents.Settle = workflow.Settlement{Tries: 1}
	dbtest.FailNextStatement(t, a.db, "-- name: RecordAgentTranscript")
	a.work.ExecuteStep(a.ctx, stepID)
	status, ev := stepStatus(t, a, stepID)
	if status != workflow.StatusError || !strings.Contains(fmt.Sprint(ev["reason"]), "recording the session's history") {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	if n := len(f.requests()); n != 1 {
		t.Fatalf("pi ran %d times after its history went unrecorded", n)
	}
}

// Of an old run's attempts sharing a session, only the latest is given the
// folder's file (it holds that attempt's history); an earlier one is
// recorded unavailable rather than credited with later turns.
func TestAnImportGivesTheFolderFileOnlyToTheSessionsLatestAttempt(t *testing.T) {
	t.Parallel()
	a, runID, stepID := admittedAgentStep(t, `{"prompt":"do it"}`)
	installFakePi(a, func(n int, req execution.Request) execution.Outcome {
		piKeepsSession(t, req, fmt.Sprintf("turn %d", n))
		return execution.Outcome{Output: piSays("done")}
	})
	a.work.ExecuteStep(a.ctx, stepID)
	if _, err := a.work.RetryStep(a.ctx, workflow.RetryCommand{StepID: stepID, Mode: workflow.RetryContinue, Feedback: "again"}); err != nil {
		t.Fatal(err)
	}
	a.work.ExecuteStep(a.ctx, stepID)
	latest, err := keptTranscript(t, a, runID, stepID, 2)
	if err != nil {
		t.Fatal(err)
	}
	// sql-guarantee: A run from before agent_transcripts has start receipts and no records; no operation removes a record, so they are deleted to construct that era.
	if _, err := a.db.Exec("DELETE FROM agent_transcripts WHERE step_id=$1", stepID); err != nil {
		t.Fatal(err)
	}
	a.agents.BackfillTranscripts(context.Background())
	if got, err := keptTranscript(t, a, runID, stepID, 2); err != nil || got != latest {
		t.Fatalf("the latest attempt: %q %v", got, err)
	}
	if _, err := keptTranscript(t, a, runID, stepID, 1); err == nil || !strings.Contains(err.Error(), "a later attempt continued its session") {
		t.Fatalf("the earlier attempt: %v", err)
	}
}

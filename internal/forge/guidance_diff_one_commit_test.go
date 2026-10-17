package forge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
)

// A diff rubric in a guidance run must judge the same commit as its source
// checks, including unpublished work and a branch that moves after staging.
func TestGuidanceDiffRubricsJudgeTheCheckedCommit(t *testing.T) {
	t.Parallel()
	for _, moving := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpublished", true: "moving-branch"}[moving], func(t *testing.T) {
			t.Parallel()
			a, ch := step1ReviewGuidance(t, true, `[{"name":"source","evaluator":"sh","sh":"true"},{"name":"diff","evaluator":"llm","scope":"diff","rubric":"judge the proposed change","expects":"the rule holds"}]`)
			bindFastSlot(t, a)
			var prompts []string
			stubLLM(t, a, `[{"item":1,"verdict":"pass","evidence":"inspected diff","note":"ok"}]`, &prompts)
			ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(ch.ID)
			repo := a.projectRepoPath("step1-review")
			publish := func() { gittest.Run(t, repo, "fetch", ws, "HEAD:refs/heads/"+ch.Branch) }
			seen := false
			installCheckEffects(t, a, checkOverrides{run: func(req ExecRequest) ExecOutcome {
				if !seen {
					seen = true
					body, err := os.ReadFile(filepath.Join(req.Workspace, "source.txt"))
					if err != nil || string(body) != "checked-first\n" {
						t.Fatalf("staged tree = %q, %v", body, err)
					}
					if moving {
						writeFile(t, ws, "source.txt", "later-second\n")
						commitFolder(t, ws)
						publish()
					}
				}
				return ExecOutcome{}
			}})
			run := step1ReviewRun(t, a, ch)
			publish() // The project copy initially holds the base, before this local work.
			writeFile(t, ws, "source.txt", "checked-first\n")
			commitFolder(t, ws)
			checked := gittest.Run(t, ws, "rev-parse", "HEAD")
			if moving {
				publish()
			}
			saved := reviewExecute(t, a, run, "guidance")
			var ev struct{ Commit string }
			if err := json.Unmarshal(saved.Evidence, &ev); err != nil {
				t.Fatal(err)
			}
			if !seen || ev.Commit != checked {
				t.Fatalf("source identity: seen=%t checked=%s evidence=%s", seen, checked, saved.Evidence)
			}
			if len(prompts) != 1 || !strings.Contains(prompts[0], "+checked-first") || strings.Contains(prompts[0], "+later-second") {
				t.Fatalf("diff does not describe checked commit %s: prompts=%q status=%s evidence=%s", checked, prompts, saved.Status, saved.Evidence)
			}
		})
	}
}

// Acceptance uses the selected commit's changes and ignore rules, even when
// a writer repairs or introduces ignored files after staging.
func TestTheAcceptanceDiffIsTheCheckedCommits(t *testing.T) {
	t.Parallel()
	for _, bad := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean-then-dirty", true: "dirty-then-clean"}[bad], func(t *testing.T) {
			t.Parallel()
			a, ch := step1ReviewGuidance(t, true, `[{"name":"source","evaluator":"sh","sh":"true"}]`)
			ws := (workflow.Layout{DataDir: a.cfg.dataDir}).ChangeWorkspace(ch.ID)
			moved := false
			installCheckEffects(t, a, checkOverrides{run: func(req ExecRequest) ExecOutcome {
				if !moved {
					moved = true
					if bad {
						gittest.Run(t, ws, "rm", "-q", "artifacts/output.txt")
						writeFile(t, ws, ".gitignore", "")
					} else {
						writeFile(t, ws, "artifacts/output.txt", "later generated output\n")
					}
					commitFolder(t, ws)
				}
				return ExecOutcome{}
			}})
			run := step1ReviewRun(t, a, ch)
			writeFile(t, ws, ".gitignore", "artifacts/\n")
			writeFile(t, ws, "source.txt", "selected source\n")
			if bad {
				writeFile(t, ws, "artifacts/output.txt", "selected generated output\n")
			}
			commitFolder(t, ws)
			checked := gittest.Run(t, ws, "rev-parse", "HEAD")
			saved := reviewExecute(t, a, run, "guidance")
			want := workflow.StatusPass
			if bad {
				want = workflow.StatusFail
			}
			var ev struct{ Commit string }
			if err := json.Unmarshal(saved.Evidence, &ev); err != nil {
				t.Fatal(err)
			}
			if !moved || saved.Status != want || ev.Commit != checked {
				t.Fatalf("want %s on %s, got %s: %s", want, checked, saved.Status, saved.Evidence)
			}
			if bad && !strings.Contains(string(saved.Evidence), "artifacts/output.txt") {
				t.Fatalf("missing ignored file: %s", saved.Evidence)
			}
		})
	}
}

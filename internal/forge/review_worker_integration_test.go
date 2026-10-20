//go:build integration

package forge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/tasks"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec/gittest"
)

// Review steps on a real worker (plan step 6): apptown-worker's server in this
// process, running its tasks on this machine's Docker in the fake Pi image
// (fakePiScript), whose behaviour each change's fake-pi.sh sets. The forge's
// own agent process is a tripwire.

// Shell the fake reviewer runs: pass writes a valid passing report, as a
// reviewer does, in the tree's .forge.
const reviewPass = `mkdir -p .forge; printf '{"verdict":"pass","summary":"fine","findings":[]}' > .forge/review.json; `

func newReviewWorkerWorld(t *testing.T, with map[string]any, files map[string]string) *agentWorkerWorld {
	t.Helper()
	step := map[string]any{"prompt": "check it"}
	for k, v := range with {
		step[k] = v
	}
	return newWorkerStepWorld(t, schema.Step{Name: "review", Uses: "agent/review", With: step}, files)
}

// names are the attempt's invocations, in order.
func names(invocations []tasks.Attempt) string {
	var out []string
	for _, inv := range invocations {
		out = append(out, inv.Invocation)
	}
	return strings.Join(out, ",")
}

// A review on a forge with a worker runs there, in the attempt's workspace,
// as one invocation "review" of its own session, Pi pointed at its exact
// transcript file in the state folder. Its verdict is its report, read as a declared result. The
// reviewer committed nothing: its candidate is its source, and the branch
// does not move. Its usage and transcript are kept; its invocation and
// workspace are released. Nothing runs on the forge's own machine.
func TestIntegrationAReviewRunsOnTheWorker(t *testing.T) {
	t.Parallel()
	w := newReviewWorkerWorld(t, nil, map[string]string{"fake-pi.sh": reviewPass + `say "reviewed with: $PI_ARGS"`})
	hub := w.source.Repository(w.a.cfg.dataDir)
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	if status != workflow.StatusPass || ev["verdict"] != "pass" || ev["summary"] != "fine" {
		t.Fatalf("the review is %s: %v", status, ev)
	}
	result, _ := ev["result"].(map[string]any)
	if ev["worker"] != "worker at "+w.cfg.Worker.URL || ev["image"] != w.image || ev["piVersion"] != fakePiVersion || result["commit"] != ev["source"] || ev["resumed"] != nil {
		t.Fatalf("the evidence: %v", ev)
	}
	if tip := scm.Ref(t.Context(), hub, "refs/heads/"+w.source.Branch); tip != ev["source"] {
		t.Fatalf("the branch is at %s, the review's source %v", tip, ev["source"])
	}
	awaitAttemptReleased(t, w, 1)
	invocations := w.invocations(t, 1)
	if names(invocations) != "review" || invocations[0].Observed != tasks.Settled || invocations[0].Workspace != fmt.Sprintf("step-%d-a1", w.step.ID) {
		t.Fatalf("the attempt's invocations: %+v", invocations)
	}
	if spent, found, err := w.a.Usage.Ledger.Observed(context.Background(), invocations[0].Operation); err != nil || !found || spent.Totals.Total != 15 {
		t.Fatalf("the review's usage: %+v %v %v", spent, found, err)
	}
	session := fmt.Sprintf("step-%d-a1", w.step.ID)
	receipt, raw := w.transcript(t, 1)
	if receipt.Session != session || !bytes.Contains(raw, []byte("--session /apptown/state/sessions/")) || !bytes.Contains(raw, []byte("_"+session+".jsonl ")) ||
		bytes.Contains(raw, []byte("--session-id")) || bytes.Contains(raw, []byte("--continue")) || !bytes.Contains(raw, []byte("THE ACTUAL DIFF")) {
		t.Fatalf("the kept transcript: %+v %s", receipt, raw)
	}
}

// The scope guard reads the whole change, from the change's base to the
// candidate, in the hub: an implementer's commit of a file the repository
// ignores fails the review even though the reviewer passed it and
// committed nothing.
func TestIntegrationAReviewOnTheWorkerCatchesTheImplementersScope(t *testing.T) {
	t.Parallel()
	w := newReviewWorkerWorld(t, nil, map[string]string{".gitignore": ".forge/*\n!.forge/app.yaml\ntarget/\n", "fake-pi.sh": reviewPass + `say "looks fine"`})
	ws := w.source.Workspace(w.a.cfg.dataDir)
	if err := os.MkdirAll(filepath.Join(ws, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "target", "out.class"), []byte("built"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "-f", "--", "target/out.class")
	gittest.Run(t, ws, "-c", "user.name=alice", "-c", "user.email=alice@example.org", "commit", "-q", "-m", "the implementer's build output")
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	findings := fmt.Sprint(ev["findings"])
	if status != workflow.StatusFail || ev["verdict"] != "fail" || !strings.Contains(findings, "ignored files committed: target/out.class") {
		t.Fatalf("the review is %s: %v", status, ev)
	}
	saw, _ := ev["scopeSaw"].(map[string]any)
	result, _ := ev["result"].(map[string]any)
	if !strings.Contains(fmt.Sprint(saw["files"]), "target/out.class") || result["commit"] != ev["source"] {
		t.Fatalf("the evidence: %v", ev)
	}
}

// A review on the worker publishes nothing: what the reviewer left in its
// workspace (a cache from running the tests, an edit to a tracked file) is
// collected into its candidate, kept as evidence, and never moves the
// branch; the scope guard judges the change as the reviewer was given it,
// not the reviewer's leftovers.
func TestIntegrationAReviewOnTheWorkerPublishesNothingItLeft(t *testing.T) {
	t.Parallel()
	w := newReviewWorkerWorld(t, nil, map[string]string{"fake-pi.sh": reviewPass + `mkdir -p __pycache__; echo cache > __pycache__/app.pyc; echo scratch >> README.md; say "reviewed"`})
	hub := w.source.Repository(w.a.cfg.dataDir)
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	if status != workflow.StatusPass || ev["verdict"] != "pass" {
		t.Fatalf("the review is %s: %v", status, ev)
	}
	result, _ := ev["result"].(map[string]any)
	if result["commit"] == ev["source"] {
		t.Fatalf("the reviewer's leftovers were not collected into its candidate: %v", ev)
	}
	if tip := scm.Ref(t.Context(), hub, "refs/heads/"+w.source.Branch); tip != ev["source"] {
		t.Fatalf("the review moved the branch to %s; it was given %v", tip, ev["source"])
	}
	saw, _ := ev["scopeSaw"].(map[string]any)
	if files := fmt.Sprint(saw["files"]); strings.Contains(files, "__pycache__") || strings.Contains(files, "README.md") {
		t.Fatalf("the scope guard judged the reviewer's leftovers as the change's: %v", files)
	}
}

// The verdict table on a real worker: each case's status, reason and the
// invocations it ran. A verdict counts only from an invocation that exited
// 0 with no provider error and a present, valid report; the wrap-up or
// fixup is one more invocation at most, by the same rules.
func TestIntegrationTheReviewVerdictTableOnTheWorker(t *testing.T) {
	t.Parallel()
	providerError := `echo '{"type":"message_end","message":{"role":"assistant","model":"fake-model","provider":"fake","stopReason":"error","errorMessage":"credit balance too low","content":[],"usage":{"input":1,"output":0,"totalTokens":1}}}'; `
	for name, c := range map[string]struct {
		script      string
		status      workflow.StepState
		reason      string
		invocations string
	}{
		"an invalid report, fixed": {script: `if [ "$PI_INVOCATION" = 1 ]; then mkdir -p .forge; echo '{"verdict":' > .forge/review.json; else ` + reviewPass + `fi; say "ok"`,
			status: workflow.StatusPass, invocations: "review,review-fixup"},
		"an invalid report, twice": {script: `mkdir -p .forge; echo '{"verdict":"maybe"}' > .forge/review.json; say "ok"`,
			status: workflow.StatusError, reason: "reviewer verdict unparseable", invocations: "review,review-fixup"},
		"an oversized report": {script: `mkdir -p .forge; head -c 300000 /dev/zero | tr '\000' ' ' > .forge/review.json; say "ok"`,
			status: workflow.StatusError, reason: "over the 256 KiB", invocations: "review"},
		"a report behind a linked folder": {script: `mkdir -p /apptown/state/elsewhere; printf '{"verdict":"pass","summary":"s","findings":[]}' > /apptown/state/elsewhere/review.json; rm -rf .forge; ln -s /apptown/state/elsewhere .forge; say "ok"`,
			status: workflow.StatusError, reason: "refused to read it (symlink)", invocations: "review"},
		"a report linked to its parent": {script: `printf '{"verdict":"pass","summary":"s","findings":[]}' > /apptown/state/review.json; mkdir -p .forge; ln -s ../../apptown/state/review.json .forge/review.json; say "ok"`,
			status: workflow.StatusError, reason: "refused to read it", invocations: "review"},
		"a pass, then exit 1": {script: reviewPass + `say "ok"; exit 1`,
			status: workflow.StatusError, reason: "exited with code 1", invocations: "review"},
		"no report, then a wrap-up's pass and exit 1": {script: `if [ "$PI_INVOCATION" = 2 ]; then ` + reviewPass + `say "wrapped"; exit 1; fi; say "forgot"`,
			status: workflow.StatusError, reason: "review-wrapup exited with code 1", invocations: "review,review-wrapup"},
		"a provider error beside a pass": {script: reviewPass + providerError,
			status: workflow.StatusError, reason: "provider refused the request: credit balance too low", invocations: "review"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newReviewWorkerWorld(t, nil, map[string]string{"fake-pi.sh": c.script})
			w.execute(t, w.a)
			status, ev := stepStatus(t, w.a, w.step.ID)
			if reason, _ := ev["reason"].(string); status != c.status || !strings.Contains(reason, c.reason) {
				t.Fatalf("the review is %s: %v", status, ev)
			}
			if c.status != workflow.StatusPass && ev["verdict"] != nil {
				t.Fatalf("a verdict counted: %v", ev)
			}
			if got := names(w.invocations(t, 1)); got != c.invocations {
				t.Fatalf("invocations %s, want %s", got, c.invocations)
			}
			if resumed := ev["resumed"] == true; resumed != strings.Contains(c.invocations, ",") {
				t.Fatalf("resumed: %v", ev)
			}
			awaitAttemptReleased(t, w, 1)
		})
	}
}

// A review that times out, its stop confirmed, earns one wrap-up, whose
// report counts. A pass the timed-out invocation left in the tree is seeded
// before the wrap-up starts, and never counts: the report is Fresh. (The
// branch cannot carry a seed: .forge/review.json is not source, and the
// run's source is refused with it.)
func TestIntegrationATimedOutReviewIsWrappedUpOnce(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		wrapUp string
		status workflow.StepState
		reason string
	}{
		"the wrap-up writes its verdict":            {wrapUp: reviewPass + `say "wrapped: $PI_PROMPT"`, status: workflow.StatusPass},
		"the wrap-up writes nothing, a pass seeded": {wrapUp: `say "nothing"`, status: workflow.StatusError, reason: "no .forge/review.json verdict"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newReviewWorkerWorld(t, map[string]any{"timeoutMinutes": 1}, map[string]string{
				"fake-pi.sh": `if [ "$PI_INVOCATION" = 1 ]; then ` + reviewPass + `say "started"; sleep 150; fi; ` + c.wrapUp})
			w.execute(t, w.a)
			status, ev := stepStatus(t, w.a, w.step.ID)
			if reason, _ := ev["reason"].(string); status != c.status || !strings.Contains(reason, c.reason) || ev["resumed"] != true {
				t.Fatalf("the review is %s: %v", status, ev)
			}
			invocations := w.invocations(t, 1)
			if names(invocations) != "review,review-wrapup" || invocations[0].ExitCode == nil || *invocations[0].ExitCode != -1 {
				t.Fatalf("invocations: %+v", invocations)
			}
			// The wrap-up's budget is its allowance, recorded when reserved.
			if budget := invocations[1].Admitted.Limits.WallClock; budget != 5*time.Minute {
				t.Fatalf("the wrap-up's budget: %v", budget)
			}
		})
	}
}

// A review that times out and whose stop the worker does not confirm has an
// unknown outcome: the attempt is an error, nothing more starts, and its
// workspace is held.
func TestIntegrationATimedOutReviewWhoseStopIsUnknownStartsNothingMore(t *testing.T) {
	t.Parallel()
	w := newReviewWorkerWorld(t, map[string]any{"timeoutMinutes": 1}, map[string]string{"fake-pi.sh": reviewPass + `sleep 600`})
	w.refuseStop.Store(true)
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	if reason, _ := ev["reason"].(string); status != workflow.StatusError || !strings.Contains(reason, "outcome is unknown") || ev["verdict"] != nil {
		t.Fatalf("the review is %s: %v", status, ev)
	}
	invocations := w.invocations(t, 1)
	if names(invocations) != "review" || invocations[0].Released || invocations[0].WorkspaceReleased {
		t.Fatalf("invocations: %+v", invocations)
	}
	w.refuseStop.Store(false)
}

// The forge that recorded the review's exit is gone before it read the
// report: a restarted forge reads it again (the worker captured it at
// exit) and settles the review once, with one invocation.
func TestIntegrationAReviewsReportIsReadAgainAfterARestart(t *testing.T) {
	t.Parallel()
	w := newReviewWorkerWorld(t, nil, map[string]string{"fake-pi.sh": reviewPass + `say "reviewed $PI_INVOCATION"`})
	w.a.tasks.AbandonAt = func(stage string) bool { return stage == "exited" }
	w.execute(t, w.a)
	if status, ev := stepStatus(t, w.a, w.step.ID); status != workflow.StatusRunning {
		t.Fatalf("before the restart the review is %s: %v", status, ev)
	}
	b := w.restart(t)
	settled := awaitStep(t, b, w.step.ID, workflow.StatusPass)
	if !bytes.Contains(settled.Evidence, []byte(`"verdict":"pass"`)) {
		t.Fatalf("the evidence: %s", settled.Evidence)
	}
	awaitAttemptReleased(t, w, 1)
	if got := names(w.invocations(t, 1)); got != "review" {
		t.Fatalf("invocations: %s", got)
	}
	if _, raw := w.transcript(t, 1); bytes.Count(raw, []byte("reviewed 1")) != 1 || bytes.Contains(raw, []byte("reviewed 2")) {
		t.Fatalf("the reviewer ran again: %s", raw)
	}
}

// A wrap-up resumes the review's own session, its exact transcript file
// (--session), never "the newest" nor a lookup by id: newer files left in
// the session folder, one of another session and one naming the review's
// own, are not what it continues, and the kept transcript holds both turns
// of the review's session and nothing of the others.
func TestIntegrationAWrapUpResumesTheReviewsOwnSession(t *testing.T) {
	t.Parallel()
	w := newReviewWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `
if [ "$PI_INVOCATION" = 1 ]; then
  printf '{"type":"session","version":3,"id":"someone-else"}\n{"competing":true}\n' > /apptown/state/sessions/9999-99-99_someone-else.jsonl
  printf '{"type":"session","version":3,"id":"%s"}\n{"competing":true}\n' "$PI_SESSION" > "/apptown/state/sessions/9999-99-99_$PI_SESSION.jsonl"
  say "forgot the report"
else
  ` + reviewPass + `say "wrap-up of $PI_SESSION: $PI_ARGS"
fi`})
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	if status != workflow.StatusPass || ev["resumed"] != true {
		t.Fatalf("the review is %s: %v", status, ev)
	}
	session := fmt.Sprintf("step-%d-a1", w.step.ID)
	_, raw := w.transcript(t, 1)
	if !bytes.Contains(raw, []byte("forgot the report")) || !bytes.Contains(raw, []byte("wrap-up of "+session+": ")) ||
		!bytes.Contains(raw, []byte("--session /apptown/state/sessions/")) || !bytes.Contains(raw, []byte("_"+session+".jsonl ")) ||
		bytes.Contains(raw, []byte("--session-id")) || bytes.Contains(raw, []byte("--continue")) || bytes.Contains(raw, []byte("competing")) {
		t.Fatalf("the kept transcript: %s", raw)
	}
	if !bytes.Contains(raw, []byte("TIME IS UP")) {
		t.Fatalf("the wrap-up was not given its order: %s", raw)
	}
	invocations := w.invocations(t, 1)
	if names(invocations) != "review,review-wrapup" {
		t.Fatalf("invocations: %+v", invocations)
	}
	for _, inv := range invocations {
		if spent, found, err := w.a.Usage.Ledger.Observed(context.Background(), inv.Operation); err != nil || !found || spent.Totals.Total == 0 {
			t.Fatalf("%s's usage: %+v %v %v", inv.Invocation, spent, found, err)
		}
	}
}

// A change whose base the hub cannot give cannot be reviewed: the review is
// an error that says so before the reviewer is started, never an empty
// scope and never a pass. A change's base is where it forks from main
// (scm.ChangeBase); only without main does the recorded base stand in, so
// the hub here has lost its main and the recorded base is one it lacks (a
// hub restored without them, say).
func TestIntegrationAReviewWhoseScopeCannotBeReadIsNotAssessed(t *testing.T) {
	t.Parallel()
	w := newReviewWorkerWorld(t, nil, map[string]string{"fake-pi.sh": reviewPass + `say "fine"`})
	if err := w.a.DB.InTx(context.Background(), func(tx *database.Tx) error {
		// sql-guarantee: No operation records a base the project's hub lacks, which is the state under test (a hub restored without it, say).
		_, err := tx.SQL().ExecContext(context.Background(), `UPDATE changes SET base_sha=$1 WHERE id=$2`, strings.Repeat("ab", 20), w.run.ChangeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, w.source.Repository(w.a.cfg.dataDir), "update-ref", "-d", "refs/heads/"+w.source.DefaultBranch)
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	// On a worker the reviewer's brief carries the change's diff, read
	// from the same base before anything starts, so that read is the one
	// that refuses (workflowexec's review_test covers the scope's words).
	if reason, _ := ev["reason"].(string); status != workflow.StatusError || !strings.Contains(reason, "reading the change's diff for the reviewer ("+strings.Repeat("ab", 20)+"...") || ev["verdict"] != nil {
		t.Fatalf("the review is %s: %v", status, ev)
	}
	if invocations := w.invocations(t, 1); len(invocations) != 0 {
		t.Fatalf("the reviewer was started: %+v", invocations)
	}
}

// A force-added ignored file must be refused even when its name contains
// spaces, and the evidence names it exactly.
func TestReviewS6ScopePreservesPathsWithSpaces(t *testing.T) {
	t.Parallel()
	w := newReviewWorkerWorld(t, nil, map[string]string{".gitignore": ".forge/*\n!.forge/app.yaml\ntarget/\n", "fake-pi.sh": reviewPass + `say "looks fine"`})
	ws := w.source.Workspace(w.a.cfg.dataDir)
	if err := os.MkdirAll(filepath.Join(ws, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "target", "out file.class"), []byte("built"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, ws, "add", "-f", "--", "target/out file.class")
	gittest.Run(t, ws, "-c", "user.name=alice", "-c", "user.email=alice@example.org", "commit", "-q", "-m", "the implementer's build output")
	w.execute(t, w.a)
	status, ev := stepStatus(t, w.a, w.step.ID)
	findings := fmt.Sprint(ev["findings"])
	if status != workflow.StatusFail || ev["verdict"] != "fail" || !strings.Contains(findings, "ignored files committed: target/out file.class") {
		t.Fatalf("the review is %s: %v", status, ev)
	}
	saw, _ := ev["scopeSaw"].(map[string]any)
	result, _ := ev["result"].(map[string]any)
	if !strings.Contains(fmt.Sprint(saw["files"]), "target/out file.class") || result["commit"] != ev["source"] {
		t.Fatalf("the evidence: %v", ev)
	}
}

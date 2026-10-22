//go:build integration

package forge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/repositoryedit"
	"flokkit.io/forge/internal/forge/schema"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitexec/gittest"
)

// A change's branch behind main is caught up by AppTown before its agent
// step on a worker: main is merged in (an ordinary merge, AppTown's, the
// change's work then main), the step works on that merge and sees main's
// files, its work builds on it, the run records the merge, and the branch
// then contains main, as the landing's check requires.
func TestIntegrationTheForgeMergesMainBeforeAWorkerStep(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, nil, map[string]string{"fake-pi.sh": `test -f later.txt && echo "saw main" > saw-main.txt; say "done"`})
	hub := w.source.Repository(w.a.cfg.dataDir)
	if err := os.WriteFile(filepath.Join(hub, "later.txt"), []byte("main moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stageAll(hub)
	if out, err := gitexec.Run(t.Context(), hub, "commit", "-m", "main moves"); err != nil {
		t.Fatalf("main moves: %s", out)
	}
	main := gittest.Run(t, hub, "rev-parse", "refs/heads/main")

	w.execute(t, w.a)
	if status, ev := stepStatus(t, w.a, w.step.ID); status != workflow.StatusPass {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	invocations := w.invocations(t, 1)
	if len(invocations) != 1 {
		t.Fatalf("the attempt's invocations: %+v", invocations)
	}
	given := invocations[0].Source
	parents := strings.Fields(gittest.Run(t, hub, "rev-list", "--parents", "-n", "1", given))
	if len(parents) != 3 || parents[2] != main {
		t.Fatalf("the step was given %v, not a merge of main", parents)
	}
	if who := gittest.Run(t, hub, "log", "-1", "--format=%an %cn %s", given); who != "apptown apptown Merge main into "+w.source.Branch {
		t.Fatalf("the merge is %q", who)
	}
	if got := w.branchFile(t, "saw-main.txt"); got != "saw main" {
		t.Fatalf("the step did not see main's files: %q", got)
	}
	tip := gittest.Run(t, hub, "rev-parse", "refs/heads/"+w.source.Branch)
	if !gitexec.HubAncestor(t.Context(), hub, given, tip) || !gitexec.HubAncestor(t.Context(), hub, main, tip) {
		t.Fatal("the step's work does not build on the merge, so the branch is still behind main")
	}

	snapshot, err := w.a.Work.Snapshot(asPerson("alice"), w.run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	var merged []map[string]any
	for _, e := range snapshot.Events {
		if e.Kind == repositoryedit.MainMergedNotice {
			var payload map[string]any
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			merged = append(merged, payload)
		}
	}
	if len(merged) != 1 || merged[0]["by"] != "apptown-system" || merged[0]["main"] != main || merged[0]["sha"] != given || merged[0]["step"] != w.step.Name {
		t.Fatalf("the run's record of the merge: %+v", merged)
	}
}

// Main that conflicts with the change is not merged: the step works on the
// change as it is, the run records the conflict, and the change's page
// payload says it, with the files and the catch-up, until the branch
// contains that main.
func TestIntegrationMainThatConflictsIsShownOnTheChange(t *testing.T) {
	t.Parallel()
	w := newAgentWorkerWorld(t, nil, map[string]string{"app.txt": "the change's app\n", "fake-pi.sh": `say "done"`})
	hub := w.source.Repository(w.a.cfg.dataDir)
	if err := os.WriteFile(filepath.Join(hub, "app.txt"), []byte("main's app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stageAll(hub)
	if out, err := gitexec.Run(t.Context(), hub, "commit", "-m", "main moves"); err != nil {
		t.Fatalf("main moves: %s", out)
	}
	main := gittest.Run(t, hub, "rev-parse", "refs/heads/main")

	w.execute(t, w.a)
	if status, ev := stepStatus(t, w.a, w.step.ID); status != workflow.StatusPass {
		t.Fatalf("the step is %s: %v", status, ev)
	}
	given := w.invocations(t, 1)[0].Source
	if parents := strings.Fields(gittest.Run(t, hub, "rev-list", "--parents", "-n", "1", given)); len(parents) != 2 {
		t.Fatalf("the step was given a merge despite the conflict: %v", parents)
	}

	rec := changeRequest(w.a, "alice", "GET", "/api/v1/changes/"+w.run.ChangeID.String(), "")
	var view struct {
		MainConflict *repositoryedit.MainConflict `json:"mainConflict"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &view) != nil || view.MainConflict == nil {
		t.Fatalf("the change's payload: %d %s", rec.Code, rec.Body)
	}
	if c := view.MainConflict; c.Main != main || c.Step != w.step.Name || len(c.Files) != 1 || c.Files[0] != "app.txt" ||
		c.CatchUp != repositoryedit.CatchUpCommand("main", w.source.Branch) {
		t.Fatalf("the change's conflict: %+v", c)
	}

	// The work is done and a landing is tried: it finds the branch behind
	// main and ends, the change back in progress with the landing as its
	// run. The conflict is still the change's, and still shown.
	ctx := asPerson("alice")
	if out, err := w.a.Work.Advance(ctx, w.run.ID); err != nil || out.RunStatus != workflow.RunDone {
		t.Fatalf("finishing the work: %+v %v", out, err)
	}
	defineWorkflows(t, w.a, workflow.PreparedRun{Workflow: workflow.DefinitionRef{Name: "land", Version: 1}, Spec: schema.Spec{Kind: "pipeline", Role: "landing",
		Stages: []schema.Stage{{Name: "gates", Steps: []schema.Step{{Name: "catch-up-with-main", Uses: "reconcile"}}}}}})
	w.a.Work.Stop()
	landing, err := w.a.Work.StartRun(ctx, workflow.StartChangeRun{ChangeID: w.run.ChangeID, Workflow: "land"})
	if err != nil && !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	snapshot, err := w.a.Work.Snapshot(ctx, landing.ID, workflow.DetailView)
	if err != nil || len(snapshot.Steps) != 1 {
		t.Fatalf("the landing's steps: %+v %v", snapshot, err)
	}
	if err := w.a.Work.ExecuteStep(w.a.asEngine(context.Background()), snapshot.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	back, err := w.a.Work.Change(ctx, w.run.ChangeID)
	if err != nil || back.State != workflow.ChangeInProgress || back.RunID != landing.ID {
		t.Fatalf("the change after its refused landing: %+v %v", back, err)
	}
	rec = changeRequest(w.a, "alice", "GET", "/api/v1/changes/"+w.run.ChangeID.String(), "")
	view.MainConflict = nil
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &view) != nil || view.MainConflict == nil || view.MainConflict.Main != main {
		t.Fatalf("the conflict after the refused landing: %d %s", rec.Code, rec.Body)
	}
}

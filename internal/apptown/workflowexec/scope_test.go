package workflowexec

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/imageline"
	"github.com/flokkit/apptown/stock"
)

func TestScopeGivesTheRoutineJudgementWithoutChangingTheRun(t *testing.T) {
	raw, _ := stock.FS.ReadFile("janitor-fix.yaml")
	doc, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newNodeFixFixture(t, "fix", doc.Spec)
	updateWrite(t, f.hub, "REPORT.md", "original report\n")
	updateWrite(t, f.hub, "mise.toml", "[tools]\ngo='1.27.3'\n")
	updateWrite(t, f.hub, "Dockerfile", "FROM golang:1.27.3 AS build\nFROM scratch\n")
	updateCommit(t, f.hub, "tracked report and image")
	// Use the existing image proof fixture: the judgement sees the complete diff.
	f.commands.images = testImageReadings{answer: imagelines.Reading{Answer: imageline.Answer{Tag: "golang:1.27.4", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
		updateWrite(t, req.Workspace, "Dockerfile", "FROM golang:1.27.4@sha256:"+strings.Repeat("a", 64)+" AS build\nFROM scratch\n")
		updateWrite(t, req.Workspace, "mise.toml", "[tools]\ngo='1.27.4'\n")
		updateWrite(t, req.Workspace, "REPORT.md", "unasked rewrite\n")
		updateCommit(t, req.Workspace, "patch and stray report")
		return execution.Outcome{Termination: execution.Exited, Output: piSays("committed")}
	})
	var items []check.Item
	f.commands.judge = updateJudgeFunc(func(_ context.Context, _ workflow.Source, item check.Item, diff string) (workflow.CheckReceipt, error) {
		items = append(items, item)
		for _, fact := range []string{"Approved level: routine", "Base image updates: on", "Toolchain patches: on", "toolchain: Dockerfile, mise.toml", "other: REPORT.md"} {
			if !strings.Contains(item.Rubric, fact) {
				t.Errorf("judge lacks %q: %s", fact, item.Rubric)
			}
		}
		if !strings.Contains(diff, "unasked rewrite") {
			t.Fatal("judge did not see report diff")
		}
		return workflow.CheckReceipt{Verdict: workflow.VerdictFail, Detail: "Modifies REPORT.md"}, nil
	})
	run := queueRun(t, f, "fix", "updates")
	steps := queueSteps(t, f, run)
	queueExecute(t, f, steps["update"])
	ws := (workflow.Layout{DataDir: f.dir}).ChangeWorkspace(run.ChangeID)
	head := gittest.Run(t, ws, "rev-parse", "HEAD")
	before, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.commands.Scope(f.ctx, run.ID, head)
	if err != nil || out.Status != workflow.StatusFail || !strings.HasPrefix(out.Reason, "Modifies REPORT.md") || out.Commit != head || !strings.Contains(out.Facts, "other: REPORT.md") || !strings.Contains(out.Brief, "Base image updates: on") {
		t.Fatalf("scope: %+v %v", out, err)
	}
	after, err := f.work.Snapshot(f.ctx, run.ID, workflow.DetailView)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("feedback changed run: %v", err)
	}
	attempts, err := f.work.Attempts(f.ctx, f.projectID, "updates", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.commands.Scope(f.ctx, run.ID, strings.Repeat("ab", 20)); err == nil || len(items) != 1 {
		t.Fatalf("unrelated commit judged: %v", err)
	}
	again, _ := f.work.Attempts(f.ctx, f.projectID, "updates", 10)
	if !reflect.DeepEqual(attempts, again) {
		t.Fatal("feedback changed attempt")
	}
	// Exhaust the existing repair budget through actual repair starts.
	for n := 0; n < 3; n++ {
		steps = queueSteps(t, f, run)
		queueExecute(t, f, steps["routine"])
		if status, ev := stepStatus(t, f, steps["routine"].ID); status != workflow.StatusFail || ev["reason"] != out.Reason {
			t.Fatalf("routine: %s %+v", status, ev)
		}
		if _, err := f.work.Advance(f.ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		if n < 2 {
			steps = queueSteps(t, f, run)
			// Preserve the same candidate while consuming a repair attempt.
			installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
				piKeepsSession(t, req, "kept required fix")
				return execution.Outcome{Termination: execution.Exited, Output: piSays("kept required fix")}
			})
			queueExecute(t, f, steps["repair"])
			if _, err := f.work.Advance(f.ctx, run.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(items) != 4 || !reflect.DeepEqual(items[0], items[1]) {
		t.Fatalf("routine received different judgement input: %d", len(items))
	}
	attempts, err = f.work.Attempts(f.ctx, f.projectID, "updates", 10)
	body, _ := json.Marshal(attempts)
	if err != nil || !strings.Contains(string(body), "routine: Modifies REPORT.md") {
		t.Fatalf("not held: %s %v", body, err)
	}
	// A new attempt proposes the held tree again. Suppression must precede
	// registry access as well as the judge.
	f.commands.images = testImageReadings{err: errors.New("must not query registry for duplicate")}
	f.commands.judge = updateJudgeFunc(func(context.Context, workflow.Source, check.Item, string) (workflow.CheckReceipt, error) {
		t.Fatal("duplicate called judge")
		return workflow.CheckReceipt{}, nil
	})
	installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
		updateWrite(t, req.Workspace, "Dockerfile", "FROM golang:1.27.4@sha256:"+strings.Repeat("a", 64)+" AS build\nFROM scratch\n")
		updateWrite(t, req.Workspace, "mise.toml", "[tools]\ngo='1.27.4'\n")
		updateWrite(t, req.Workspace, "REPORT.md", "unasked rewrite\n")
		updateCommit(t, req.Workspace, "same proposal")
		return execution.Outcome{Termination: execution.Exited, Output: piSays("committed")}
	})
	duplicate := queueRun(t, f, "fix", "updates")
	steps = queueSteps(t, f, duplicate)
	queueExecute(t, f, steps["update"])
	queueExecute(t, f, steps["routine"])
	if status, ev := stepStatus(t, f, steps["routine"].ID); status != workflow.StatusSkipped || ev["sameProposal"] != true {
		t.Fatalf("duplicate was judged: %s %+v", status, ev)
	}

}

func TestScopeEmptyDiffNamesSettingsWithoutAJudge(t *testing.T) {
	raw, _ := stock.FS.ReadFile("janitor-update.yaml")
	doc, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newNodeFixFixture(t, "updates", doc.Spec)
	installFakePi(f, func(_ int, _ execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, Output: piSays("nothing changed")}
	})
	f.commands.judge = updateJudgeFunc(func(context.Context, workflow.Source, check.Item, string) (workflow.CheckReceipt, error) {
		t.Fatal("empty diff called judge")
		return workflow.CheckReceipt{}, nil
	})
	run := queueRun(t, f, "updates", "updates")
	queueExecute(t, f, queueSteps(t, f, run)["update"])
	head := gittest.Run(t, (workflow.Layout{DataDir: f.dir}).ChangeWorkspace(run.ChangeID), "rev-parse", "HEAD")
	for _, images := range []bool{false, true} {
		f.commands.bounds = scopeBounds{workflow.JanitorChoices{Dependencies: "routine", BaseImages: images}}
		out, err := f.commands.Scope(f.ctx, run.ID, head)
		want := "off"
		if images {
			want = "on"
		}
		if err != nil || out.Status != workflow.StatusNotApplicable || !strings.Contains(out.Brief, "Base image updates: "+want) || !strings.Contains(out.Brief, "Approved level: routine") || !strings.Contains(out.Brief, "Toolchain patches: off") {
			t.Fatalf("empty scope: %+v %v", out, err)
		}
	}
}

type scopeBounds struct{ choices workflow.JanitorChoices }

func (b scopeBounds) UpdateChoicesForRun(context.Context, ids.ID) (workflow.JanitorChoices, error) {
	return b.choices, nil
}

func TestScopeDoesNotJudgePackAdoption(t *testing.T) {
	raw, _ := stock.FS.ReadFile("janitor-adopt.yaml")
	doc, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	var rules workflow.StandingRules
	f, _ := newTaskAppTown(t, "agent", doc.Spec, func(deps *workflow.Dependencies) { deps.Standing = &rules })
	rules = workflow.NewStandingRules(f.db, workflow.StandingDependencies{Adoptions: adoption.New(f.db, adoption.Dependencies{Ways: agentCatalog{}})})
	f.commands.judge = updateJudgeFunc(func(context.Context, workflow.Source, check.Item, string) (workflow.CheckReceipt, error) {
		t.Fatal("adoption called dependency judge")
		return workflow.CheckReceipt{}, nil
	})
	f.work.Stop()
	ch, err := openAdoptionChange(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.work.PrepareWorkspace(f.ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	run, err := f.work.StartRun(f.ctx, workflow.StartChangeRun{ChangeID: ch.ID, Workflow: "agent"})
	if !errors.Is(err, workflow.ErrDispatchStopped) {
		t.Fatal(err)
	}
	ws := (workflow.Layout{DataDir: f.dir}).ChangeWorkspace(ch.ID)
	updateWrite(t, ws, ".apptown/app.yaml", "app:\n  web: {static: public, public: true}\n")
	updateCommit(t, ws, "contract repair required by pack")
	head := gittest.Run(t, ws, "rev-parse", "HEAD")
	out, err := f.commands.Scope(f.ctx, run.ID, head)
	if err != nil || out.Status != workflow.StatusNotApplicable || out.Reason != "dependency settings do not apply to this run" {
		t.Fatalf("adoption scope: %+v %v", out, err)
	}
	queueExecute(t, f, queueSteps(t, f, run)["routine"])
	if status, ev := stepStatus(t, f, queueSteps(t, f, run)["routine"].ID); status != workflow.StatusPass {
		t.Fatalf("adoption routine changed: %s %+v", status, ev)
	}
}

// A malformed dependency input is an inability to assess, not a scope verdict.
func TestScopeReportsUnreadableDependenciesAsAnError(t *testing.T) {
	raw, _ := stock.FS.ReadFile("janitor-update.yaml")
	doc, err := schema.ParseWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newNodeFixFixture(t, "updates", doc.Spec)
	installFakePi(f, func(_ int, req execution.Request) execution.Outcome {
		updateWrite(t, req.Workspace, "package-lock.json", "{broken lockfile\n")
		updateCommit(t, req.Workspace, "incomplete lockfile repair")
		return execution.Outcome{Termination: execution.Exited, Output: piSays("committed")}
	})
	f.commands.judge = updateJudgeFunc(func(context.Context, workflow.Source, check.Item, string) (workflow.CheckReceipt, error) {
		t.Fatal("unreadable dependency input reached judge")
		return workflow.CheckReceipt{}, nil
	})
	run := queueRun(t, f, "updates", "updates")
	queueExecute(t, f, queueSteps(t, f, run)["update"])
	head := gittest.Run(t, (workflow.Layout{DataDir: f.dir}).ChangeWorkspace(run.ChangeID), "rev-parse", "HEAD")
	out, err := f.commands.Scope(f.ctx, run.ID, head)
	if err == nil {
		t.Fatalf("inability to read dependencies returned as scope verdict: %+v", out)
	}
}

package workflowexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/preview"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids"
)

func TestReviewPreviewEvidenceSuppliesTheRecordedPNGsAndResourceFailures(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	var e *Executor
	work, ctx, stepIDs, _, _ := reviewFixtureSteps(t, []schema.Step{{Name: "review", Uses: ReviewKind}}, nil, func(executor *Executor) { e = executor })
	step, err := work.Step(ctx, stepIDs["review"])
	if err != nil {
		t.Fatal(err)
	}
	run, err := workflow.ReadRunIn(ctx, e.db, step.Step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\npicture")
	id, err := workflow.AddArtifactIn(t.Context(), e.db, run.ID, screenshotKind, "/ desktop", "", string(png), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"screenshots": map[string]any{"commit": "abc", "shots": []map[string]any{{"artifact": id, "path": "/", "view": "desktop"}}, "failedResources": []map[string]any{{"path": "/", "view": "desktop", "type": "Stylesheet", "url": "dead.example/style.css", "status": 404}}}})
	if err != nil {
		t.Fatal(err)
	}
	steps := []*workflow.Step{{ID: ids.New(time.Now()), RunID: run.ID, Type: "preview", Status: workflow.StatusPass, Evidence: raw}}
	note, files, err := e.previewEvidence(t.Context(), run, steps, "def")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"not from raw HTML", "Preview commit: abc", "1 resource failed to load", "Commit under review: def", "pictures may be stale", "dead.example/style.css", "404", "/apptown/in/preview/1.png"} {
		if !strings.Contains(note, want) {
			t.Errorf("preview note lacks %q: %s", want, note)
		}
	}
	if len(files) != 1 || string(files[0].Content) != string(png) {
		t.Fatalf("files: %+v", files)
	}
	// The worker gets the same PNG bytes, as read-only task inputs.
	f.call.invocation = "review"
	f.call.review = &reviewCall{brief: reviewBrief{notes: note, files: files}}
	req := f.build(t)
	in, ok := inputsByPath(t, req.spec)[files[0].Path]
	if !ok || string(inputContent(t, in)) != string(png) {
		t.Fatal("worker review did not receive the PNG")
	}
	if !strings.Contains(req.spec.Argv[len(req.spec.Argv)-1], note) {
		t.Fatal("worker review did not receive the preview note")
	}
	// Missing evidence and unreadable artifacts are never a visual pass.
	steps[0].Evidence = json.RawMessage(`{"screenshots":{"unavailable":"browser failed"}}`)
	note, _, err = e.previewEvidence(t.Context(), run, steps, "def")
	if err != nil || !strings.Contains(note, "browser failed") || !strings.Contains(note, "appearance could not be assessed") {
		t.Fatalf("missing pictures: %s %v", note, err)
	}
	steps[0].Evidence = raw
	other := *run
	other.ProjectID = ids.New(time.Now())
	if _, _, err := e.previewEvidence(t.Context(), &other, steps, "def"); err == nil {
		t.Fatal("screenshot crossed project scope")
	}
}

// Even a review before a later preview gets a complete brief. There are no
// pictures yet, so the pending preview must not be read as broken evidence.
func TestReviewBeforePreviewDoesNotReadPendingEvidence(t *testing.T) {
	var e *Executor
	work, ctx, stepIDs, execute, _ := reviewFixtureSteps(t, []schema.Step{{Name: "review", Uses: ReviewKind}, {Name: "boot", Uses: "preview"}}, nil, func(executor *Executor) { e = executor })
	step, err := work.Step(ctx, stepIDs["review"])
	if err != nil {
		t.Fatal(err)
	}
	run, err := workflow.ReadRunIn(ctx, e.db, step.Step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.reviewBrief(ctx, run, workflow.Source{}, reviewConfig{}, "", "", func(context.Context) (string, []repositoryedit.ChangedFile, error) { return "", nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.prompt(), "APPTOWN PREVIEW EVIDENCE") {
		t.Fatal("unrun preview presented as evidence")
	}
	execute("boot")
	b, err = e.reviewBrief(ctx, run, workflow.Source{}, reviewConfig{}, "", "", func(context.Context) (string, []repositoryedit.ChangedFile, error) { return "", nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.prompt(), "No screenshots were recorded") {
		t.Fatal("shared review brief omitted preview evidence")
	}
}

// Run the local review after a preview has recorded its PNG, rather than
// calling piInvoke directly: Review must pass the brief's files through.
func TestLocalReviewReceivesTheRecordedPreviewPNG(t *testing.T) {
	t.Parallel()
	var e *Executor
	png := []byte("\x89PNG\r\n\x1a\npicture")
	calls := 0
	work, ctx, steps, execute, _ := reviewFixtureSteps(t, []schema.Step{{Name: "boot", Uses: "preview"}, {Name: "review", Uses: ReviewKind}}, func(req execution.Request) execution.Outcome {
		calls++
		found := false
		for _, file := range req.Files {
			if file.Path == "/apptown/in/preview/1.png" && string(file.Content) == string(png) {
				found = true
			}
		}
		if !found {
			t.Error("local review lost the recorded preview PNG")
		}
		if !strings.Contains(req.Argv[len(req.Argv)-1], "/apptown/in/preview/1.png") {
			t.Error("review prompt lacks PNG path")
		}
		return reviewFile(t, req, `{"verdict":"pass","summary":"checked","findings":[]}`)
	}, func(executor *Executor) { e = executor }, func(task *workflow.Execution) {
		if _, err := task.Claim(); err != nil {
			t.Fatal(err)
		}
		id, err := workflow.AddArtifactIn(t.Context(), e.db, task.Step().RunID, screenshotKind, "/ desktop", "", string(png), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{"screenshots": map[string]any{"commit": "abc", "shots": []map[string]any{{"artifact": id, "path": "/", "view": "desktop"}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := task.Finish(workflow.StepOutcome{Status: workflow.StatusPass, Evidence: raw}, nil); err != nil {
			t.Fatal(err)
		}
	})
	execute("boot")
	execute("review")
	step, err := work.Step(ctx, steps["review"])
	if err != nil || step.Step.Status != workflow.StatusPass || calls != 1 {
		t.Fatalf("review: %+v, %v; calls %d", step, err, calls)
	}
}

func TestReviewCapsResourceFailuresAndNamesBothCommits(t *testing.T) {
	t.Parallel()
	e := &Executor{}
	for _, n := range []int{0, 1, 50, 53} {
		failures := make([]map[string]any, n)
		for i := range failures {
			failures[i] = map[string]any{"url": fmt.Sprintf("example.test/%d", i)}
		}
		raw, err := json.Marshal(map[string]any{"screenshots": map[string]any{"commit": "abc", "failedResources": failures, "failedResourcesMore": 2}})
		if err != nil {
			t.Fatal(err)
		}
		steps := []*workflow.Step{{Type: "preview", Status: workflow.StatusPass, Evidence: raw}}
		note, _, err := e.previewEvidence(t.Context(), &workflow.Run{}, steps, "abc")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(note, "Preview commit: abc") || !strings.Contains(note, "Commit under review: abc") || strings.Contains(note, "may be stale") {
			t.Fatalf("commit comparison: %s", note)
		}
		more := 2 + max(0, n-50)
		if !strings.Contains(note, fmt.Sprintf("%d resources failed to load", n+2)) || !strings.Contains(note, fmt.Sprintf("and %d more", more)) || strings.Contains(note, "example.test/50") {
			t.Fatalf("unbounded failures: %s", note)
		}
	}
}

// The omitted count must survive the handoff from capture to stored evidence,
// including a capture that could not take a picture.
func TestPreviewEvidenceRetainsTheOmittedResourceCount(t *testing.T) {
	t.Parallel()
	var e *Executor
	work, ctx, steps, _, _ := reviewFixtureSteps(t, []schema.Step{{Name: "review", Uses: ReviewKind}}, nil, func(executor *Executor) { e = executor })
	step, err := work.Step(ctx, steps["review"])
	if err != nil {
		t.Fatal(err)
	}
	run, err := workflow.ReadRunIn(ctx, e.db, step.Step.RunID)
	if err != nil {
		t.Fatal(err)
	}
	failures := make([]verification.FailedResource, 53)
	report, err := json.Marshal(map[string]any{"pages": []map[string]any{{"name": "1-desktop", "failedResources": failures}}})
	if err != nil {
		t.Fatal(err)
	}
	capture := verification.NewScreenshots(verification.Executor{AgentImage: "agent", Run: func(context.Context, execution.Request) execution.Outcome {
		return execution.Outcome{Termination: execution.Exited, Report: report}
	}}, nil)
	env := deployment.NewEnvironment(deployment.EnvironmentDependencies{Routes: deployment.Routing{PreviewDomain: "preview.example"}})
	ship := &shipping{db: e.db, shots: capture, previews: preview.New(e.db, preview.Dependencies{Environment: env})}
	evidence := ship.screenshots(ctx, run, "https://app.preview.example/", "abc")
	if len(evidence["failedResources"].([]verification.FailedResource)) != 50 || evidence["failedResourcesMore"] != 3 {
		t.Fatalf("capture evidence lost the bound or count: %v", evidence)
	}
	raw, err := json.Marshal(map[string]any{"screenshots": evidence})
	if err != nil {
		t.Fatal(err)
	}
	note, _, err := e.previewEvidence(ctx, run, []*workflow.Step{{Type: "preview", Status: workflow.StatusPass, Evidence: raw}}, "def")
	if err != nil || !strings.Contains(note, "53 resources failed to load") || !strings.Contains(note, "and 3 more") {
		t.Fatalf("review lost the omitted count: %s %v", note, err)
	}
}

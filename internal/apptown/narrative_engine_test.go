package apptown

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/riverqueue/river/rivertype"
)

// After a run ends, AppTown refreshes the change's "So far" summary as
// itself and it publishes, attributed to AppTown. That authority reaches
// the summary only: AppTown still cannot edit the person-written brief.
func TestAppTownsNarrativeRefreshPublishesAfterARunEndsButNeverTheBrief(t *testing.T) {
	t.Parallel()
	a := agentTestApp(t, Config{DockerClient: newFakeDocker()})
	runID, _ := admittedRunnerStep(t, a, "nar", "work", "agent/pi", true)
	if out, err := a.Work.Advance(asPerson("alice"), runID); err != nil || out.RunStatus != workflow.RunDone {
		t.Fatalf("the run ends: %+v %v", out, err)
	}
	calls := 0
	b := narrativeBuilder{work: a.Work,
		resolve: func(context.Context, ids.ID) (string, string) { return "openrouter/fixture", "fast" },
		evidence: func(context.Context, workflow.NarrativeSource) (narrativeEvidence, error) {
			return narrativeEvidence{Text: "DIFF: one line"}, nil
		},
		models: askFunc(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
			calls++
			return "TITLE: Runner lifecycle\nASKED: yes\n\nThe runner now finishes its work.", &modelcall.Usage{}, nil
		}),
	}
	followUps := workflowFollowUps(a)
	followUps.Narratives = b
	if _, err := followUps.Complete(context.Background(), fmt.Sprintf("%d/%s", runID, workflow.CompletionNarrative), nil); err != nil {
		t.Fatal(err)
	}
	snap, err := a.Work.Snapshot(asPerson("alice"), runID, workflow.DetailView)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := a.Work.Change(asPerson("alice"), snap.Run.ChangeID)
	if err != nil || calls != 1 || ch.Summary != "The runner now finishes its work." {
		t.Fatalf("the automatic narrative: %q after %d model call(s), %v", ch.Summary, calls, err)
	}
	entries, err := audit.Recent(context.Background(), a.DB, audit.Filter{KindPrefix: "change.narrative-updated"})
	if err != nil || len(entries) != 1 || entries[0].Actor != "apptown-system" {
		t.Fatalf("the narrative is recorded as AppTown's: %+v %v", entries, err)
	}
	body, rev := "AppTown's words", ch.EditRevision
	_, err = a.Work.Edit(a.asEngine(context.Background()), workflow.ChangeEdit{ProjectID: ch.ProjectID, ID: ch.ID, Body: &body, ExpectedRevision: &rev})
	var denied *access.Denied
	if !errors.As(err, &denied) || !strings.Contains(err.Error(), "brief") {
		t.Fatalf("AppTown edited the person's brief: %v", err)
	}
	if after, _ := a.Work.Change(asPerson("alice"), ch.ID); after.Body != ch.Body {
		t.Fatalf("the brief changed: %q", after.Body)
	}
}

// A refresh asked for after a chat or an import is one queue job per
// Change, run as AppTown: two requests while it waits queue one job, and
// running it publishes the narrative attributed to AppTown. A refresh that
// fails ends its job failed, not done.
// Kills: requests that pile up; a job that refreshes as nobody (refused) or
// reports a failed refresh as done.
func TestANarrativeRequestIsOneJobRunAsAppTown(t *testing.T) {
	t.Parallel()
	a := testApp(t)
	change := createReadTestChange(t, a, createReadTestProject(t, a, "told"), "told")
	var refused error
	a.narratives.builder = narrativeBuilder{work: a.Work,
		resolve: func(context.Context, ids.ID) (string, string) { return "openrouter/fixture", "fast" },
		evidence: func(context.Context, workflow.NarrativeSource) (narrativeEvidence, error) {
			return narrativeEvidence{Text: "DIFF: one line"}, nil
		},
		models: askFunc(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
			return "TITLE: Told\nASKED: yes\n\nThe story so far.", &modelcall.Usage{}, refused
		}),
	}
	key := fmt.Sprintf("change/%d", change)
	for range 2 {
		if err := a.narratives.Request(context.Background(), change); err != nil {
			t.Fatal(err)
		}
	}
	if jobs := operationJobs(t, a, narrativeJob); len(jobs) != 1 || jobs[0].Key != key {
		t.Fatalf("two requests queued %+v", jobs)
	}
	if row := runQueuedJobForTest(t, a, narrativeJob, key); row.State != rivertype.JobStateCompleted {
		t.Fatalf("the refresh job ended %s", row.State)
	}
	ch, err := a.Work.Change(asPerson("bls"), change)
	if err != nil || ch.Summary != "The story so far." {
		t.Fatalf("the narrative: %q %v", ch.Summary, err)
	}
	entries, err := audit.Recent(context.Background(), a.DB, audit.Filter{KindPrefix: "change.narrative-updated"})
	if err != nil || len(entries) != 1 || entries[0].Actor != "apptown-system" {
		t.Fatalf("the narrative is recorded as AppTown's: %+v %v", entries, err)
	}
	refused = errors.New("the model is unavailable")
	if err := a.narratives.Request(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	if row := runQueuedJobForTest(t, a, narrativeJob, key); row.State == rivertype.JobStateCompleted {
		t.Fatal("a failed refresh ended its job done")
	}
}

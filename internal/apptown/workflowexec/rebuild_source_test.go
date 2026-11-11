package workflowexec

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestARebuildRunPinsItsRecordedCommitRatherThanCurrentMain(t *testing.T) {
	t.Parallel()
	spec := schema.Spec{Kind: "pipeline", Role: "record", Workspace: "run", Stages: []schema.Stage{{Name: "build", Steps: []schema.Step{{Name: "build", Uses: BuildKind}}}}}
	f, _ := newTaskAppTown(t, "rebuild", spec)
	commit := gittest.Run(t, f.hub, "rev-parse", "HEAD")
	hubCommit(t, f.hub, map[string]string{"later": "main moved after the original release"})
	system := access.WithCaller(context.Background(), access.Caller{Verified: true, Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "engine"}})
	runID, err := f.work.StartRebuild(system, f.projectID, idstest.N(800), commit, 7)
	if err != nil {
		t.Fatal(err)
	}
	run, err := workflow.ReadRunIn(t.Context(), f.db, runID)
	if err != nil || run.SourceCommit != commit || run.RebuildFrom != 7 {
		t.Fatalf("pinned rebuild: %+v %v", run, err)
	}
	source, err := workflow.ReadSourceIn(t.Context(), f.db, workflow.RunRef{ID: runID, ProjectID: f.projectID})
	if err != nil || source.SourceCommit != commit {
		t.Fatalf("pinned source: %+v %v", source, err)
	}
	path, err := source.Workspace(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := gittest.Run(t, path, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("rebuilt current main %s, wanted %s", got, commit)
	}
	again, err := f.work.StartRebuild(system, f.projectID, idstest.N(800), commit, 7)
	if err != nil || again != runID {
		t.Fatalf("redelivery duplicated rebuild: %s %v", again, err)
	}
}

package apptown

import (
	"context"
	"log"
	"sort"

	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflowexec"
)

// Automatic runner kinds come from the execution registry; SQL binds this value.
func autoStepTypes() []string {
	keys := []string{workflowexec.CallKind, workflowexec.MergeRequestKind, workflowexec.ReconcileKind, workflowexec.DeployKind, workflowexec.PreviewKind, workflowexec.ScriptKind, workflowexec.TestsKind, workflowexec.AgentKind, workflowexec.ReviewKind, verification.ScanKind, verification.GuidanceKind}
	sort.Strings(keys)
	return keys
}

// ResumePendingAutoSteps restores complete stage plans and uses the same
// dispatcher as normal entry, including failed prerequisites and scan groups.
// Tasks the previous process recorded are sorted first: one whose step moved
// on is stopped; one whose step still runs is continued by the dispatcher in
// its place in the stage, and its run continues when it settles. Agent
// attempts that recorded no history have it imported first, so a resumed
// attempt continuing one reads it through its receipt.
func (a *App) ResumePendingAutoSteps(ctx context.Context) {
	a.agents.BackfillTranscripts(ctx)
	adopt, err := a.tasks.Resume(a.asEngine(ctx), a.Work)
	if err != nil {
		log.Printf("startup repair: resuming recorded tasks: %v", err)
	}
	a.Work.ResumePending(adopt...)
}

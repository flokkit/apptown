package repositoryedit

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
)

// MainMergedNotice is the run's record that AppTown merged main into the
// change's branch before a step (PublishMainMerge).
const MainMergedNotice = "main.merged"

// MainConflictNotice is the run's record that main could not be merged into
// the change's branch before a step, because files conflict. It is the
// fact a person reads (MainConflictIn); nobody is sent it.
const MainConflictNotice = "main.conflict"

// MergeTitle is the subject of a merge of main into a change's branch, the
// forge's or a chat's (CatchUpCommand).
func MergeTitle(mainBranch, branch string) string {
	return fmt.Sprintf("Merge %s into %s", mainBranch, branch)
}

// CatchUpCommand is how a change's chat catches up with main itself.
func CatchUpCommand(mainBranch, branch string) string {
	return fmt.Sprintf(`git fetch "$APPTOWN_MAIN_REMOTE" %s && git merge --no-edit -m %q FETCH_HEAD`, mainBranch, MergeTitle(mainBranch, branch))
}

// MainConflict is main that AppTown could not merge into a change, while
// the change's branch still does not contain it.
type MainConflict struct {
	MainBranch string   `json:"mainBranch"`
	Main       string   `json:"main"`
	Step       string   `json:"step"`
	Files      []string `json:"files"`
	// CatchUp is the command that catches the change up in its chat.
	CatchUp string `json:"catchUp"`
}

// MainConflictIn is the change's latest main.conflict (latest, the newest
// across all its runs: workflow.Service.LatestChangeEvent), while the
// change's branch in hub does not contain the main it names; nil once it
// does (the change caught up, in a chat or by a later merge), or when there
// is none. main only moves forward, so a branch containing the latest
// conflicting main contains every earlier one. A branch that cannot be read
// is not said to contain it.
func MainConflictIn(ctx context.Context, hub, branch string, latest *workflow.Event) *MainConflict {
	if latest == nil || latest.Kind != MainConflictNotice {
		return nil
	}
	var payload struct {
		MainBranch string   `json:"mainBranch"`
		Main       string   `json:"main"`
		Step       string   `json:"step"`
		Conflicts  []string `json:"conflicts"`
	}
	if json.Unmarshal(latest.Payload, &payload) != nil || !gitexec.IsObjectID(payload.Main) {
		return nil
	}
	if gitexec.HubAncestor(ctx, hub, payload.Main, "refs/heads/"+branch) {
		return nil
	}
	if payload.Conflicts == nil {
		payload.Conflicts = []string{}
	}
	return &MainConflict{MainBranch: payload.MainBranch, Main: payload.Main, Step: payload.Step, Files: payload.Conflicts,
		CatchUp: CatchUpCommand(payload.MainBranch, branch)}
}

// PublishMainMerge publishes AppTown's merge of main into the run's branch
// (scm.MergeMain), made on from, the branch's tip, before step: the branch
// moves from from to the merge by compare-and-swap in the hub, under the
// authority the step's attempt was admitted with, as the step's own source
// is published. A branch that moved since, or a newer holder, refuses it,
// and the error says so. Once it is published, the run records it
// (main.merged: AppTown, main's tip, the merge, the step) and the run's
// folder is brought forward to it; a folder that cannot be is noted as for
// a task's result. folder is the run's folder, whose lock the caller holds.
func PublishMainMerge(ctx context.Context, db *database.DB, dataDir string, branches *scm.Branches, publisher Publisher, run *workflow.Run, step *workflow.Step, folder *gitexec.Folder, from string, merge scm.MainMerge, at time.Time) error {
	source, err := workflow.ReadSourceIn(ctx, db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return err
	}
	repo, wt := source.Repository(dataDir), source.Workspace(dataDir)
	if folder == nil || folder.Path() != filepath.Clean(wt) {
		return fmt.Errorf("publishing the merge of %s needs the run's folder %s locked", source.DefaultBranch, wt)
	}
	// The folder is behind while the branch holds the merge and the folder
	// does not, so readers read the branch (as for a task's result).
	if err := scm.MarkSaved(ctx, wt, repo, source.Branch, merge.Commit); err != nil {
		return fmt.Errorf("recording the merge of %s for the run's folder: %w", source.DefaultBranch, err)
	}
	if _, err := branches.Publish(ctx, scm.Publication{ProjectID: run.ProjectID, Repository: repo, Branch: source.Branch, Commit: merge.Commit, Expected: from,
		Grant: scm.Grant{Generation: step.SourceGeneration()}}); err != nil {
		return fmt.Errorf("publishing the merge of %s into %s: %w", source.DefaultBranch, source.Branch, err)
	}
	workflow.RecordNoticeOrLog(ctx, db, run.ID, MainMergedNotice, map[string]any{
		"by": "apptown-system", "step": step.Name, "change": source.ChangeID, "branch": source.Branch,
		"mainBranch": source.DefaultBranch, "main": merge.Main, "sha": merge.Commit}, at)
	publisher.PushBranch(ctx, run.ProjectID, repo, source.Branch)
	bringForward(ctx, db, run, step, folder, repo, source.Branch, merge.Commit, at)
	return nil
}

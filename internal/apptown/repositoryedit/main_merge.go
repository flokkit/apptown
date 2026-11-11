package repositoryedit

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
)

// MainMergedNotice is the run's record that AppTown merged main into the
// change's branch before a step (PublishMainMerge).
const MainMergedNotice = "main.merged"

// MainConflictNotice is the run's record that main could not be merged into
// the change's branch before a step, because files conflict. Nobody is
// sent it; the change page asks Git (MainConflictNow).
const MainConflictNotice = "main.conflict"

// MergeTitle is the subject of a merge of main into a change's branch, the
// AppTown's or a chat's (CatchUpCommand).
func MergeTitle(mainBranch, branch string) string {
	return fmt.Sprintf("Merge %s into %s", mainBranch, branch)
}

// CatchUpCommand is how a change's chat catches up with main itself.
func CatchUpCommand(mainBranch, branch string) string {
	return fmt.Sprintf(`git fetch "$APPTOWN_MAIN_REMOTE" %s && git merge --no-edit -m %q FETCH_HEAD`, mainBranch, MergeTitle(mainBranch, branch))
}

// MainConflict is main that does not merge cleanly into a change's
// branch: the files that conflict, while the branch does not contain main.
type MainConflict struct {
	MainBranch string   `json:"mainBranch"`
	Main       string   `json:"main"`
	Files      []string `json:"files"`
	// CatchUp is the command that catches the change up in its chat.
	CatchUp string `json:"catchUp"`
}

// MainConflictNow is whether main, as it is now, conflicts with the
// change's branch in hub: nil when the branch contains main or main merges
// in cleanly (a landing then merges it in itself), the files otherwise.
// It is asked before anyone presses "Check and land", so a change that
// could only end its landing at the first step is sent to its chat
// instead. Git failing to answer is the error, never "no conflict".
func MainConflictNow(ctx context.Context, hub, branch, mainBranch string) (*MainConflict, error) {
	tip := scm.Ref(ctx, hub, "refs/heads/"+branch)
	if tip == "" || mainBranch == "" {
		return nil, nil
	}
	merge, err := scm.MainConflicts(ctx, hub, tip, mainBranch)
	if err != nil || len(merge.Conflicts) == 0 {
		return nil, err
	}
	return &MainConflict{MainBranch: mainBranch, Main: merge.Main, Files: merge.Conflicts, CatchUp: CatchUpCommand(mainBranch, branch)}, nil
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
	repo, err := source.Repository(dataDir)
	if err != nil {
		return err
	}
	wt, err := source.Workspace(dataDir)
	if err != nil {
		return err
	}
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

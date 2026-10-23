package workflowexec

import (
	"context"
	"fmt"
	"log"

	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/trailer"
)

// AppTown catches a change up with main itself. Before an agent step of a
// change's run starts, on a worker or on AppTown's own machine, when the
// change's branch is behind main,
// AppTown merges main into it, if the merge has no conflicts and the run
// still holds the branch under the step's attempt; the step then works on
// the merge. The rule is the same before every such step, so no step has to
// know why the branch fell behind (a landing that ended behind main, or main
// moving during the run). A merge with conflicts is not made: the step works
// on the branch as it is, and the run records which files conflict
// (main.conflict). That record is what a person reads: the change page and
// apptown wait -c show it, with the catch-up command, while the branch still
// does not contain that main (repositoryedit.MainConflictIn). Nobody is
// sent it: while the run holds the change no chat can be open on it. The
// landing still checks that the branch contains main (reconcile), on
// AppTown, whatever happened here. A review or a landing's step is never given a merge: a
// review judges the change as it is, and a landing publishes nothing.

// mainMergeBrief is what an agent step of a change is told about main.
const mainMergeBrief = "# Main\n\nIf main moves, AppTown merges it into this change before your step when it can; you don't need to catch up yourself.\n\n"

// mergeMainFirst is the merge of main the step at commit is to start from
// (scm.MergeMain), made but not published; a zero MainMerge when the branch
// contains main, the merge conflicts (recorded on the run), the run no longer
// holds the branch under generation, the generation the step's attempt was
// admitted under, or Git could not answer (logged; the landing still
// checks).
func (a *Executor) mergeMainFirst(ctx context.Context, run *workflow.Run, step *workflow.Step, generation int64, source workflow.Source, commit string) scm.MainMerge {
	if !mergesMain(source) {
		return scm.MainMerge{}
	}
	// Only the step's own holder moves the branch: a branch handed to a
	// chat, a desktop checkout or a newer attempt is left to it.
	authority, err := scm.ReadAuthorityIn(ctx, a.db, run.ProjectID, source.Branch)
	if err != nil || authority.Holder != scm.RunHolder(source.RootRunID) || authority.Generation != generation {
		if err != nil {
			log.Printf("run %d step %s: reading who holds %s, so main is not merged in: %v", run.ID, step.Name, source.Branch, err)
		}
		return scm.MainMerge{}
	}
	message := trailer.Message(repositoryedit.MergeTitle(source.DefaultBranch, source.Branch),
		fmt.Sprintf("AppTown merged %s into this change before step %s of run %d: the branch was behind %s and the merge had no conflicts.", source.DefaultBranch, step.Name, run.Number, source.DefaultBranch),
		trailer.Line{Key: trailer.RunID, Value: run.ID.String()}, trailer.Line{Key: trailer.Step, Value: step.Name}) + "\n"
	merge, err := scm.MergeMain(ctx, source.Repository(a.cfg.DataDir), commit, source.DefaultBranch, message)
	if err != nil {
		log.Printf("run %d step %s: main is not merged in: %v", run.ID, step.Name, err)
		return scm.MainMerge{}
	}
	if len(merge.Conflicts) > 0 {
		recordEvent(ctx, a.db, run.ID, repositoryedit.MainConflictNotice, map[string]any{"by": "apptown-system", "step": step.Name, "change": source.ChangeID,
			"branch": source.Branch, "mainBranch": source.DefaultBranch, "main": merge.Main, "conflicts": merge.Conflicts}, a.now())
		return scm.MainMerge{}
	}
	return merge
}

// mergesMain is whether an agent step of the run source names is given
// main first: a change's run, repository-backed, on its own branch, and
// not a landing's.
func mergesMain(source workflow.Source) bool {
	return !source.ChangeID.IsZero() && source.RepoBacked() && source.RootRole != "landing" && source.Branch != "" && source.Branch != source.DefaultBranch
}

// mergeMainInFolder is mergeMainFirst for an agent step that works in the
// run's folder on AppTown's own machine. As before a step on a worker, the
// folder's own commits are published first and a merge of main is
// published on them. The agent then works in the folder, so the folder
// must hold what the step starts from, the merge or the published tip,
// whether or not a merge was made now: a folder left behind is brought
// forward, and one that cannot be (files in the way) ends the step before
// the agent starts. folder is the run's folder, whose lock the caller
// holds.
func (a *Executor) mergeMainInFolder(ctx context.Context, run *workflow.Run, step *workflow.Step, source workflow.Source, folder *gitexec.Folder) error {
	if !mergesMain(source) {
		return nil
	}
	proposed, err := repositoryedit.ProposeTaskSource(ctx, a.cfg.DataDir, source)
	if err != nil {
		return err
	}
	defer proposed.Discard(ctx)
	from, err := proposed.Publish(ctx, a.branches, run, step)
	if err != nil {
		return err
	}
	start := from
	if merge := a.mergeMainFirst(ctx, run, step, step.SourceGeneration(), source, from); merge.Commit != "" {
		if err := repositoryedit.PublishMainMerge(ctx, a.db, a.cfg.DataDir, a.branches, a.publisher, run, step, folder, from, merge, a.now()); err != nil {
			return err
		}
		start = merge.Commit
	}
	holds, err := gitexec.FolderHolds(ctx, folder.Path(), source.Branch, start)
	if err == nil && !holds {
		if err = folder.FastForward(ctx, source.Repository(a.cfg.DataDir), source.Branch); err == nil {
			holds, err = gitexec.FolderHolds(ctx, folder.Path(), source.Branch, start)
		}
	}
	if err == nil && !holds {
		err = fmt.Errorf("it does not hold %s", start)
	}
	if err != nil {
		return fmt.Errorf("the run's folder could not be brought to the change's branch, so the step did not start: %w", err)
	}
	return nil
}

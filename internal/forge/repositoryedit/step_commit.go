package repositoryedit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/trailer"
)

// CommitStep commits whatever a completed step left in the worktree,
// with Forge-* trailers linking commit ↔ run ↔ step, and publishes the branch
// under the authority the step's attempt was admitted with. A refused
// publication is recorded on the run and returned; the commit stays in the
// workspace. A caller that must not pass unpublished work checks the error.
//
// It publishes whenever the published branch lacks the worktree's branch,
// not only after committing now: a commit made before a crash, or by the
// script itself, is published too. Repeating it publishes nothing twice.
func CommitStep(ctx context.Context, db *database.DB, dataDir string, branches *scm.Branches, publisher Publisher, run *workflow.Run, step *workflow.Step, at time.Time) error {
	// The step's work is committed and published though the caller stops:
	// the step has run, and a commit left unpublished is read as behind.
	ctx = context.WithoutCancel(ctx)
	stepName := step.Name
	source, err := workflow.ReadSourceIn(ctx, db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		log.Printf("run %d: selecting committed workspace: %v", run.ID, err)
		return err
	}
	// A landing only reads: whatever its steps changed stays in its own copy
	// and is not published.
	if !source.RepoBacked() || source.RootRole == "landing" {
		return nil
	}
	wt := source.Workspace(dataDir)
	sha, committed, err := scm.CommitWorkspace(ctx, wt, StepCommitMessage(run, step))
	if err != nil {
		return fmt.Errorf("committing the step's work: %w", err)
	}
	if committed {
		workflow.RecordNoticeOrLog(ctx, db, run.ID, "commit.created", map[string]string{"step": stepName, "sha": sha}, at)
	}
	repo, branch := source.Repository(dataDir), source.Branch
	tip := scm.Ref(ctx, wt, "refs/heads/"+branch)
	if tip == "" {
		return nil
	}
	if _, err := gitexec.Run(ctx, repo, "merge-base", "--is-ancestor", tip, "refs/heads/"+branch); err == nil {
		return nil // published already
	}
	sha = tip
	if _, err = branches.Publish(ctx, scm.Publication{ProjectID: run.ProjectID, Repository: repo, Branch: branch, From: wt,
		Grant: scm.Grant{Generation: step.SourceGeneration()}}); err != nil {
		log.Printf("run %d: publishing committed work: %v", run.ID, err)
		workflow.RecordNoticeOrLog(ctx, db, run.ID, "commit.unpublished", map[string]string{"step": stepName, "sha": sha, "reason": err.Error()}, at)
		return fmt.Errorf("publishing the step's commit: %w", err)
	}
	publisher.PushBranch(ctx, run.ProjectID, repo, branch)
	return nil
}

// StepCommitMessage is the message of a step's commit: its Forge-* trailers
// link the commit to its run, step and workflow.
func StepCommitMessage(run *workflow.Run, step *workflow.Step) string {
	return trailer.Message(fmt.Sprintf("forge: %s (%s run %d)", step.Name, run.WorkflowName, run.Number), "",
		trailer.Line{Key: trailer.RunID, Value: run.ID.String()},
		trailer.Line{Key: trailer.Step, Value: step.Name},
		trailer.Line{Key: trailer.Workflow, Value: fmt.Sprintf("%s@v%d", run.WorkflowName, run.WorkflowVersion)})
}

// PublishTaskSource publishes the run's committed workspace branch before a
// remote task is given it, as a step's commit is published, and returns the
// published tip: the commit the task works from (ProposeTaskSource, then
// TaskSource.Publish). Uncommitted files stay in the workspace; they are
// not part of the task's source.
func PublishTaskSource(ctx context.Context, db *database.DB, dataDir string, branches *scm.Branches, run *workflow.Run, step *workflow.Step) (string, error) {
	source, err := workflow.ReadSourceIn(ctx, db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return "", err
	}
	proposed, err := ProposeTaskSource(ctx, dataDir, source)
	if err != nil {
		return "", err
	}
	defer proposed.Discard(ctx)
	return proposed.Publish(ctx, branches, run, step)
}

// TaskSource is the commit a remote task would be given, found before
// anything is published: what may be refused for it is read at Commit
// first. When the folder's own commits are to be published they are in the
// hub already, under a private ref that moves no branch.
type TaskSource struct {
	Commit               string
	repo, branch, staged string
}

// ProposeTaskSource finds the commit a remote task of the run would be
// given, publishing nothing. The folder's branch is proposed only when the
// folder is not behind its published branch (scm.Behind) and its tip is not
// already contained in the published branch; its commits are then fetched
// into the hub under a private ref (hardened). Otherwise the task works
// from the published tip. A task is never given, or refused for, a folder
// older than the branch. No Git runs in a folder the forge refuses
// (gitexec.CheckFolder), and nothing is fetched from it. The caller
// publishes (Publish) or discards (Discard) what is proposed.
func ProposeTaskSource(ctx context.Context, dataDir string, source workflow.Source) (TaskSource, error) {
	if !source.RepoBacked() {
		return TaskSource{}, fmt.Errorf("a remote task needs a repository-backed project")
	}
	repo, wt := source.Repository(dataDir), source.Workspace(dataDir)
	out := TaskSource{repo: repo, branch: source.Branch}
	own, err := gitexec.FolderTip(ctx, wt, source.Branch)
	if errors.Is(err, gitexec.ErrFolderRefused) {
		log.Printf("run %d: AppTown runs no Git in the run's folder, so the task works from the published branch: %v", source.RunID, err)
		own = ""
	} else if err != nil {
		return TaskSource{}, fmt.Errorf("reading the run's folder: %w", err)
	}
	if own != "" {
		behind, err := scm.Behind(ctx, wt, repo)
		if err != nil {
			return TaskSource{}, fmt.Errorf("reading whether the run's folder is behind its branch: %w", err)
		}
		contained := gitexec.HubRef(ctx, repo, own+"^{commit}") != "" && gitexec.HubAncestor(ctx, repo, own, "refs/heads/"+source.Branch)
		if !behind && !contained {
			token := make([]byte, 8)
			if _, err := rand.Read(token); err != nil {
				return TaskSource{}, err
			}
			out.staged = "refs/forge/incoming/task-" + hex.EncodeToString(token)
			if msg, err := gitexec.HubRun(ctx, repo, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--end-of-options", wt, "+refs/heads/"+source.Branch+":"+out.staged); err != nil {
				out.Discard(ctx)
				return TaskSource{}, fmt.Errorf("reading the run's folder's commits: %w: %s", err, msg)
			}
			if out.Commit = gitexec.HubRef(ctx, repo, out.staged); out.Commit == "" {
				out.Discard(ctx)
				return TaskSource{}, fmt.Errorf("reading the run's folder's commits: the fetched commit is missing")
			}
			return out, nil
		}
	}
	if out.Commit = gitexec.HubRef(ctx, repo, "refs/heads/"+source.Branch); out.Commit == "" {
		out.Commit = gitexec.HubRef(ctx, repo, "refs/heads/"+source.DefaultBranch)
	}
	if out.Commit == "" {
		return TaskSource{}, fmt.Errorf("the project's repository has no commit to give the task")
	}
	return out, nil
}

// Publish publishes the folder's commits proposed (the run's branch moves
// to Commit, under the authority the step's attempt was admitted with) and
// returns the commit the task works from. A proposal of the published tip
// publishes nothing.
func (s TaskSource) Publish(ctx context.Context, branches *scm.Branches, run *workflow.Run, step *workflow.Step) (string, error) {
	if s.staged == "" {
		return s.Commit, nil
	}
	if _, err := branches.Publish(ctx, scm.Publication{ProjectID: run.ProjectID, Repository: s.repo, Branch: s.branch, Commit: s.Commit,
		Grant: scm.Grant{Generation: step.SourceGeneration()}}); err != nil {
		return "", fmt.Errorf("publishing the task's source: %w", err)
	}
	return s.Commit, nil
}

// Discard removes the private ref a proposal of the folder's commits holds
// them under, published or not; the branch and the folder are as Publish
// left them, or as they were.
// It is a clean-up, run though the caller stops.
func (s TaskSource) Discard(ctx context.Context) {
	if s.staged == "" {
		return
	}
	if msg, err := gitexec.HubRun(context.WithoutCancel(ctx), s.repo, "update-ref", "-d", s.staged); err != nil {
		log.Printf("removing the proposed task source's ref %s: %v: %s", s.staged, err, msg)
	}
}

// PublishTaskResult publishes a remote task attempt's candidate (KeepCandidate)
// under the authority the step's attempt was admitted with: the run's
// branch moves to the candidate's tip by compare-and-swap in the hub,
// expecting the commit the attempt was given. A branch that moved since, or
// a newer holder, refuses it (a stale result), as do the publication's
// checks. folder is the run's folder, whose lock the caller holds.
//
// It returns the result as it is kept afterwards: the candidate, or its
// refused ref once refused. The step's evidence names that ref.
//
// A landing publishes nothing. An attempt that changed nothing has nothing
// to publish.
//
// The result is recorded for the folder (scm.MarkSaved) before it is
// published, as a chat's save is: the folder is behind while the branch
// holds the result and the folder does not, and readers then read the
// branch. After publication the folder is brought forward to it
// (gitexec.Folder.FastForward); one that cannot be (it has its own commits,
// or it is refused) stays where it is, noted on the run.
//
// Only a refusal the branch owner confirms (scm.Refused) refuses the
// result: the candidate is renamed to its refused ref (RefusedStepRef), the
// commit.unpublished notice names the ref, the commit and the reason, and
// the refusal is returned. If the work cannot be kept, the error wraps
// ErrResultNotKept. Any other failure (a publication applied but not
// settled, one whose outcome is unknown, a failure before a decision)
// returns ErrPublicationPending with the candidate kept as it is.
//
// Repeating it publishes nothing twice. The branch is read first: a branch
// already at the candidate is the result published (before a crash, or by a
// publication not yet settled), and its notice and the folder are finished
// as the first time would, the notice once. A candidate refused before is
// answered as refused.
func PublishTaskResult(ctx context.Context, db *database.DB, dataDir string, branches *scm.Branches, publisher Publisher, run *workflow.Run, step *workflow.Step, folder *gitexec.Folder, candidate Candidate, at time.Time) (Candidate, error) {
	source, err := workflow.ReadSourceIn(ctx, db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return candidate, fmt.Errorf("%w: %w", ErrPublicationPending, err)
	}
	if source.RootRole == "landing" {
		return candidate, nil // a landing only reads: its candidate is kept, and nothing is published
	}
	if !candidate.Changed() {
		return candidate, nil
	}
	repo, wt := source.Repository(dataDir), source.Workspace(dataDir)
	if folder == nil || folder.Path() != filepath.Clean(wt) {
		return candidate, fmt.Errorf("publishing a task's result needs the run's folder %s locked", wt)
	}
	tip := candidate.Tip
	current, err := gitexec.HubReadRef(ctx, repo, "refs/heads/"+source.Branch)
	if err != nil {
		return candidate, fmt.Errorf("%w: reading the run's branch: %w", ErrPublicationPending, err)
	}
	if current == tip {
		// Published before a crash, or by a publication applied and not yet
		// settled: publish nothing again. The branch owner settles its
		// record now if it can (otherwise at the branch's next use or at
		// startup); the notice and the folder are finished.
		if err := branches.Settle(ctx, run.ProjectID, source.Branch); err != nil {
			log.Printf("run %d: settling the publication of %s: %v; the branch owner settles it later", run.ID, source.Branch, err)
		}
		published(ctx, db, publisher, run, step, folder, repo, source.Branch, tip, at)
		return candidate, nil
	}
	if candidate.Refused {
		return candidate, fmt.Errorf("publishing the task's commits was refused; they are kept under %s", candidate.Ref)
	}
	expected := candidate.Source
	if current == "" {
		expected = "" // the task started from the default branch; the branch begins here
	}
	if err := scm.MarkSaved(ctx, wt, repo, source.Branch, tip); err != nil {
		return candidate, fmt.Errorf("%w: recording the task's result for the run's folder: %w", ErrPublicationPending, err)
	}
	_, err = branches.Publish(ctx, scm.Publication{ProjectID: run.ProjectID, Repository: repo, Branch: source.Branch, Incoming: candidate.Ref, Expected: expected,
		Grant: scm.Grant{Generation: step.SourceGeneration()}})
	switch {
	case err == nil:
		published(ctx, db, publisher, run, step, folder, repo, source.Branch, tip, at)
		return candidate, nil
	case !scm.Refused(err):
		// Not a refusal: the branch may have moved, or may yet. Nothing is
		// renamed or noted; asking again reads the branch.
		log.Printf("run %d: publishing the task's commits: %v; its publication is not decided yet", run.ID, err)
		return candidate, fmt.Errorf("%w: %w", ErrPublicationPending, err)
	}
	refused, kerr := refuseCandidate(ctx, repo, candidate)
	notice := map[string]string{"step": step.Name, "sha": tip, "reason": err.Error()}
	if kerr == nil {
		notice["ref"] = refused.Ref
	}
	workflow.RecordNoticeOrLog(ctx, db, run.ID, "commit.unpublished", notice, at)
	if kerr != nil {
		return candidate, fmt.Errorf("publishing the task's commits: %v; and %w", err, kerr)
	}
	return refused, fmt.Errorf("publishing the task's commits: %w; they are kept under %s", err, refused.Ref)
}

// ErrPublicationPending: whether a task's result is published is not
// decided yet. Its candidate is kept; asking again (PublishTaskResult)
// decides from the branch, publishing nothing twice.
var ErrPublicationPending = errors.New("the task's publication is not decided yet")

// published finishes a task's published result: its commit.created notice,
// once for the step and commit, the push to the project's mirror, and the
// run's folder brought forward.
func published(ctx context.Context, db *database.DB, publisher Publisher, run *workflow.Run, step *workflow.Step, folder *gitexec.Folder, repo, branch, tip string, at time.Time) {
	payload, _ := json.Marshal(map[string]string{"step": step.Name, "sha": tip})
	same := func(earlier json.RawMessage) bool {
		var was map[string]any
		return json.Unmarshal(earlier, &was) == nil && was["step"] == step.Name && was["sha"] == tip
	}
	if err := workflow.RecordNoticeOnce(ctx, db, workflow.Notice{RunID: run.ID, Kind: "commit.created", Payload: payload, At: at}, same); err != nil {
		log.Printf("workflow event commit.created for run %d: %v", run.ID, err)
	}
	publisher.PushBranch(ctx, run.ProjectID, repo, branch)
	bringForward(ctx, db, run, step, folder, repo, branch, tip, at)
}

// bringForward brings a run's folder to its published branch after a task's
// result; one left where it is is noted on the run (folder.behind): the
// branch holds the result and readers read it.
func bringForward(ctx context.Context, db *database.DB, run *workflow.Run, step *workflow.Step, folder *gitexec.Folder, repo, branch, tip string, at time.Time) {
	err := fastForward(ctx, fmt.Sprintf("run %d", run.ID), folder, repo, branch)
	if err == nil {
		return
	}
	workflow.RecordNoticeOrLog(ctx, db, run.ID, "folder.behind", map[string]any{"step": step.Name, "sha": tip, "refused": errors.Is(err, gitexec.ErrFolderRefused), "reason": err.Error()}, at)
}

// fastForward brings a folder to its published branch, confined
// (gitexec.Folder.FastForward), when it has nothing of its own; otherwise
// the branch holds the work and the folder is left alone. A folder the
// forge runs no Git in is left alone too. who names the publisher in the
// log; the error says why the folder stayed.
func fastForward(ctx context.Context, who string, folder *gitexec.Folder, repo, branch string) error {
	err := folder.FastForward(ctx, repo, branch)
	switch {
	case errors.Is(err, gitexec.ErrFolderRefused):
		log.Printf("%s: AppTown runs no Git in the folder, so it stays where it is; the branch holds the published commits: %v", who, err)
	case err != nil:
		log.Printf("%s: the folder cannot fast-forward to the published commits; the branch holds them: %v", who, err)
	}
	return err
}

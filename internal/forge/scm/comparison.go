package scm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/strs"
)

// ComparisonRepository selects the authoritative committed history without
// publishing refs. Only an absent workspace, or one marked behind its
// published branch, permits reading the project copy; a damaged or
// inaccessible workspace must not silently show stale history.
func ComparisonRepository(ctx context.Context, project, workspace string) (string, error) {
	if _, err := os.Lstat(workspace); os.IsNotExist(err) {
		return project, nil
	} else if err != nil {
		return "", fmt.Errorf("reading comparison workspace: %w", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return "", fmt.Errorf("reading comparison workspace repository: %w", err)
	}
	if behind, err := Behind(ctx, workspace, project); err != nil || behind {
		return project, err
	}
	return workspace, nil
}

// A change's folder follows its published branch. A chat save on a worker,
// and a worker task's result, records the commit it saves before publishing
// it (MarkSaved), and the folder is behind while the branch has that commit
// and the folder does not: the save could not bring the folder along (a
// left-behind lock, or a folder AppTown will not run Git in). Then the
// branch, not the folder, is the change's work. Whatever brings the folder
// to the branch (a later save, a desktop sync, a merge) ends it, with
// nothing to clear; preparing the folder for a new writer clears the
// record, so that writer's own history, a reset included, is read as its
// work.
//
// The record is kept outside the folder, beside it
// (<workspaces>/.saved/<folder name>), so writing it never writes through a
// folder's .git, which a local writer may have changed.
func savedRecord(folder string) string {
	folder = filepath.Clean(folder)
	return filepath.Join(filepath.Dir(folder), ".saved", filepath.Base(folder))
}

// MarkSaved records that commit is being saved to branch and folder is to
// take it. A folder still behind an earlier save keeps that record: every
// later save builds on it, and a later save refused must not hide it. A
// folder that does not exist yet has nothing to record.
func MarkSaved(ctx context.Context, folder, project, branch, commit string) error {
	if _, err := os.Lstat(filepath.Join(folder, ".git")); os.IsNotExist(err) {
		return nil
	}
	if behind, err := Behind(ctx, folder, project); err != nil || behind {
		return err
	}
	record := savedRecord(folder)
	if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(record), filepath.Base(record)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(commit + " " + branch + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), record)
}

// ClearSaved forgets the recorded save: folder is prepared for a new writer
// with the branch's work in it, or is gone.
func ClearSaved(folder string) error {
	if err := os.Remove(savedRecord(folder)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Behind reports whether project's branch has the saved commit recorded for
// folder and folder does not. The project's repository is asked first, and
// a commit it does not hold is not behind, with no Git run in the folder.
// When it does hold it, a folder AppTown will not run Git in
// (gitexec.CheckFolder) is behind: its content cannot be trusted to hold
// the commit. Otherwise the folder is asked, confined.
func Behind(ctx context.Context, folder, project string) (bool, error) {
	data, err := os.ReadFile(savedRecord(folder))
	if os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("reading the folder's last saved commit: %w", err)
	}
	commit, branch, ok := strings.Cut(strings.TrimSpace(string(data)), " ")
	if !ok || commit == "" || branch == "" {
		return false, fmt.Errorf("the folder's last saved commit is unreadable: %q", data)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	saved, err := gitexec.HubHolds(ctx, project, branch, commit)
	if err != nil || !saved {
		return false, err
	}
	taken, err := gitexec.FolderHolds(ctx, folder, branch, commit)
	if errors.Is(err, gitexec.ErrFolderRefused) {
		return true, nil
	}
	return !taken, err
}

// CommittedDiff compares two resolved commits. Empty describes their net tree
// difference, not their commit count or the state of a live working directory.
// It is meaningful only after ReadCommittedDiff succeeds.
type CommittedDiff struct {
	Base, Head string
	Empty      bool
	Stat       string
}

// ChangeBase is where "what this change changes" starts: the newest commit
// of the project's default branch that the change holds, read in its hub.
// The base a change recorded when it began stops being that once the change
// catches up with main: a diff from it shows main's files as the change's
// own (a landing brief would ask why a report left out a file that came
// from main). So it is the merge-base of the default
// branch and tip, as a three-dot diff reads it. A change already landed is
// an ancestor of the default branch, where that merge-base is its own tip;
// its base is then the merge-base with main as it was before the landing
// merge (the merge's first parent). recorded stands in when the hub has no
// default branch or no history in common with tip, and for a change
// fast-forwarded onto main, which no merge records. An error is
// "could not be read", never "nothing changed".
func ChangeBase(ctx context.Context, hub, defaultBranch, tip, recorded string) (string, error) {
	main := "refs/heads/" + defaultBranch
	if defaultBranch == "" || gitexec.HubRef(ctx, hub, main+"^{commit}") == "" {
		return changeBaseRecorded(recorded, "the project has no default branch to compare with")
	}
	read := func(args ...string) (string, error) {
		out, err := gitexec.HubOutput(ctx, hub, nil, args...)
		return strings.TrimSpace(string(out)), err
	}
	fork, err := read("merge-base", "--end-of-options", main, tip)
	if err != nil {
		return changeBaseRecorded(recorded, "the change has no history in common with "+defaultBranch)
	}
	if !gitexec.HubAncestor(ctx, hub, tip, main) {
		return fork, nil
	}
	// Landed: the first commit on main's first-parent line that holds tip
	// is the merge that landed it, and main before it is its first parent.
	// A change fast-forwarded onto main (or holding nothing of its own) has
	// no such merge, and only its recorded base says where it began.
	out, err := read("rev-list", "--first-parent", "--ancestry-path", "--reverse", "--end-of-options", tip+".."+main)
	if err != nil {
		return "", fmt.Errorf("finding where the change landed: %w", err)
	}
	if landing, _, _ := strings.Cut(out, "\n"); landing != "" {
		before, err := read("merge-base", "--end-of-options", landing+"^1", tip)
		if err != nil {
			return "", fmt.Errorf("reading the change's base before it landed: %w", err)
		}
		if !gitexec.HubAncestor(ctx, hub, tip, before) {
			return before, nil
		}
	}
	return strs.FirstNonBlank(recorded, fork), nil
}

func changeBaseRecorded(recorded, why string) (string, error) {
	if recorded == "" {
		return "", errors.New("the change's base cannot be read: " + why + ", and the change records none")
	}
	return recorded, nil
}

// ReadChangeDiff is ReadCommittedDiff from the change's base (ChangeBase,
// read in the project's hub) to its branch in repo, the copy that holds its
// latest work.
func ReadChangeDiff(ctx context.Context, hub, repo, defaultBranch, recorded, branch string) (CommittedDiff, error) {
	if branch == "" {
		return CommittedDiff{}, fmt.Errorf("committed comparison needs the change's branch")
	}
	base, err := ChangeBase(ctx, hub, defaultBranch, "refs/heads/"+branch, recorded)
	if err != nil {
		return CommittedDiff{}, err
	}
	return ReadCommittedDiff(ctx, repo, base, branch)
}

// ReadCommittedDiff requires the recorded starting revision and an existing
// branch. Missing history and failed reads are errors, never empty changes.
// Both refs are resolved once so the stat and patch describe the same commits.
func ReadCommittedDiff(ctx context.Context, repo, base, branch string) (CommittedDiff, error) {
	if base == "" || branch == "" {
		return CommittedDiff{}, fmt.Errorf("committed comparison needs the change's starting revision and branch")
	}
	return compareCommits(ctx, repo, base, "refs/heads/"+branch)
}

// ReadCommitsDiff compares two commits named by revision, as
// ReadCommittedDiff compares a change's start with its branch: what the
// later one holds that the earlier did not.
func ReadCommitsDiff(ctx context.Context, repo, from, to string) (CommittedDiff, error) {
	if from == "" || to == "" {
		return CommittedDiff{}, fmt.Errorf("comparing commits needs both commits")
	}
	return compareCommits(ctx, repo, from, to)
}

func compareCommits(ctx context.Context, repo, base, head string) (CommittedDiff, error) {
	run := func(args ...string) execution.Outcome {
		return (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Workspace: repo, Argv: append([]string{"git"}, args...), Timeout: time.Minute})
	}
	git := func(args ...string) (string, error) {
		out := run(args...)
		if out.RunErr != nil {
			return "", fmt.Errorf("reading committed change: %w: %.300s", out.RunErr, out.Output)
		}
		return strings.TrimSpace(string(out.Output)), nil
	}
	from, err := git("rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return CommittedDiff{}, err
	}
	to, err := git("rev-parse", "--verify", "--end-of-options", head+"^{commit}")
	if err != nil {
		return CommittedDiff{}, err
	}
	result := CommittedDiff{Base: from, Head: to}
	out := run("diff", "--quiet", "--no-ext-diff", "--no-textconv", from, to, "--")
	if out.RunErr == nil {
		result.Empty = true
		return result, nil
	}
	if out.ExitCode != 1 || out.Termination != execution.Exited {
		return CommittedDiff{}, fmt.Errorf("comparing committed trees: %w: %.300s", out.RunErr, out.Output)
	}
	result.Stat, err = git("diff", "--shortstat", "--no-ext-diff", "--no-textconv", from, to, "--")
	if err != nil {
		return CommittedDiff{}, err
	}
	return result, nil
}

// Files is the per-file stat of the already-selected commits: one line per
// changed file with its count of changed lines, then the totals.
func (d CommittedDiff) Files(ctx context.Context, repo string) (string, error) {
	if d.Base == "" || d.Head == "" {
		return "", fmt.Errorf("a file stat requires a resolved committed comparison")
	}
	out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Workspace: repo, Argv: []string{"git", "diff", "--stat=120", "--no-ext-diff", "--no-textconv", d.Base, d.Head, "--"}, Timeout: time.Minute})
	if out.RunErr != nil {
		return "", fmt.Errorf("reading committed file stat: %w: %.300s", out.RunErr, out.Output)
	}
	return strings.TrimSpace(string(out.Output)), nil
}

// Patch reads the already-selected commits only when the caller needs the body.
// Polling the change page needs the comparison and stat, not a full patch.
func (d CommittedDiff) Patch(ctx context.Context, repo string) (string, error) {
	if d.Base == "" || d.Head == "" {
		return "", fmt.Errorf("patch requires a resolved committed comparison")
	}
	out := (execution.Runner{}).Run(ctx, execution.Local, execution.Request{Workspace: repo, Argv: []string{"git", "diff", "--no-ext-diff", "--no-textconv", d.Base, d.Head, "--"}, Timeout: time.Minute})
	if out.RunErr != nil {
		return "", fmt.Errorf("reading committed patch: %w: %.300s", out.RunErr, out.Output)
	}
	return strings.TrimSpace(string(out.Output)), nil
}

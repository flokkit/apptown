package scm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/strs"
)

// MainMerge is main merged into a change's branch by AppTown itself: the
// branch was behind main and the merge had no conflicts. Commit is the merge
// commit, made but not published; Conflicts, when it was not made, names the
// files that conflict. Main is main's tip either way. A zero MainMerge means
// there was nothing to merge.
type MainMerge struct {
	Main      string
	Commit    string
	Conflicts []string
}

// MergeMain makes, in repo (a project's hub), the commit that merges
// mainBranch's tip into tip: an ordinary merge commit with tip as its first
// parent and main as its second, AppTown as its author and committer, and
// message. Nothing moves: the caller publishes it (Branches.Publish), so who
// may move the branch is decided there. A tip that already contains main,
// or a repository without main, needs nothing. A merge with conflicts is not
// made; the files are named instead. Git failing to answer is an error. at
// (AppTown's clock) dates the commit.
func MergeMain(ctx context.Context, repo, tip, mainBranch, message string, at time.Time) (MainMerge, error) {
	out, tree, err := mergeTree(ctx, repo, tip, mainBranch)
	if err != nil || out.Main == "" || len(out.Conflicts) > 0 {
		return out, err
	}
	commit, err := gitexec.HubCommitOutput(ctx, repo, at, strings.NewReader(message), "-c", "user.name=apptown", "-c", "user.email=apptown@localhost",
		"commit-tree", tree, "-p", tip, "-p", out.Main)
	if err != nil {
		return MainMerge{}, fmt.Errorf("committing the merge of %s into %s: %w: %s", mainBranch, strs.ShortSHA(tip), err, stderrOf(err))
	}
	out.Commit = strings.TrimSpace(string(commit))
	if !gitexec.IsObjectID(out.Commit) {
		return MainMerge{}, fmt.Errorf("committing the merge of %s into %s: git named no commit (%q)", mainBranch, strs.ShortSHA(tip), out.Commit)
	}
	return out, nil
}

// MainConflicts is MergeMain without the commit: whether merging
// mainBranch's tip into tip would conflict, and in which files. A zero
// MainMerge means tip already contains main (or there is no main); Main set
// with no Conflicts means it merges cleanly. Git failing to answer is an
// error, never "no conflict".
func MainConflicts(ctx context.Context, repo, tip, mainBranch string) (MainMerge, error) {
	out, _, err := mergeTree(ctx, repo, tip, mainBranch)
	return out, err
}

// mergeTree writes the tree of merging mainBranch's tip into tip, and names
// the files that conflict instead when it does. Main is left empty when tip
// already contains main or there is no main.
func mergeTree(ctx context.Context, repo, tip, mainBranch string) (MainMerge, string, error) {
	main, err := gitexec.HubReadRef(ctx, repo, "refs/heads/"+mainBranch)
	if err != nil {
		return MainMerge{}, "", fmt.Errorf("reading %s: %w", mainBranch, err)
	}
	if main == "" || gitexec.HubAncestor(ctx, repo, main, tip) {
		return MainMerge{}, "", nil
	}
	out := MainMerge{Main: main}
	// merge-tree writes the merged tree without touching any work tree or
	// ref; it exits 1 when the merge has conflicts.
	merged, err := gitexec.HubOutput(ctx, repo, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", "--end-of-options", tip, main)
	fields := strings.Split(strings.TrimRight(string(merged), "\x00"), "\x00")
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit) && exit.ExitCode() == 1 && len(fields) > 1:
		for _, f := range fields[1:] {
			if f != "" && !slices.Contains(out.Conflicts, f) {
				out.Conflicts = append(out.Conflicts, f)
			}
		}
		return out, "", nil
	default:
		return MainMerge{}, "", fmt.Errorf("merging %s into %s: %w: %s", mainBranch, strs.ShortSHA(tip), err, stderrOf(err))
	}
	tree := fields[0]
	if !gitexec.IsObjectID(tree) {
		return MainMerge{}, "", fmt.Errorf("merging %s into %s: git wrote no tree (%q)", mainBranch, strs.ShortSHA(tip), tree)
	}
	return out, tree, nil
}

func stderrOf(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(bytes.TrimSpace(exit.Stderr))
	}
	return ""
}

// maxCatchUps bounds the merges CleanCatchUp walks: a landing is caught up
// once at admission and again at each recheck, never dozens of times.
const maxCatchUps = 16

// CleanCatchUp says why candidate is not accepted with main cleanly merged
// in, and nothing else, or "" when it is. Walking first parents from
// candidate back to accepted, every commit must be a merge of exactly two
// parents whose second is contained in target and whose tree is exactly the
// tree Git makes merging the two cleanly, as MergeMain makes a catch-up. A
// merge carrying an edit of its own, a commit that is not such a merge, or
// a history that does not reach accepted is refused: the candidate holds
// work nobody accepted. Git failing to answer is an error, never "clean".
func CleanCatchUp(ctx context.Context, repo, accepted, candidate, target string) (string, error) {
	at := candidate
	for range maxCatchUps {
		if at == accepted {
			return "", nil
		}
		parents, err := gitexec.HubOutput(ctx, repo, nil, "rev-list", "--parents", "-n", "1", "--end-of-options", at)
		if err != nil {
			return "", fmt.Errorf("reading the parents of %s: %w: %s", strs.ShortSHA(at), err, stderrOf(err))
		}
		fields := strings.Fields(string(parents))
		if len(fields) != 3 {
			return fmt.Sprintf("commit %s is not a merge of main into the accepted commit %s", strs.ShortSHA(at), strs.ShortSHA(accepted)), nil
		}
		first, second := fields[1], fields[2]
		inTarget, err := gitexec.HubIsAncestor(ctx, repo, second, target)
		if err != nil {
			return "", err
		}
		if !inTarget {
			return fmt.Sprintf("merge %s brings in %s, which is not on main", strs.ShortSHA(at), strs.ShortSHA(second)), nil
		}
		merged, err := gitexec.HubOutput(ctx, repo, nil, "merge-tree", "--write-tree", "--no-messages", "--end-of-options", first, second)
		var exit *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &exit) && exit.ExitCode() == 1:
			return fmt.Sprintf("merge %s resolved conflicts by hand", strs.ShortSHA(at)), nil
		default:
			return "", fmt.Errorf("merging the parents of %s: %w: %s", strs.ShortSHA(at), err, stderrOf(err))
		}
		tree, err := gitexec.HubReadRef(ctx, repo, at+"^{tree}")
		if err != nil {
			return "", err
		}
		if want := strings.TrimSpace(string(merged)); tree != want {
			return fmt.Sprintf("merge %s changes more than merging main in", strs.ShortSHA(at)), nil
		}
		at = first
	}
	return fmt.Sprintf("the candidate does not reach the accepted commit %s through merges of main", strs.ShortSHA(accepted)), nil
}

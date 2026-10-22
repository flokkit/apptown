package scm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

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
// made; the files are named instead. Git failing to answer is an error.
func MergeMain(ctx context.Context, repo, tip, mainBranch, message string) (MainMerge, error) {
	main, err := gitexec.HubReadRef(ctx, repo, "refs/heads/"+mainBranch)
	if err != nil {
		return MainMerge{}, fmt.Errorf("reading %s: %w", mainBranch, err)
	}
	if main == "" || gitexec.HubAncestor(ctx, repo, main, tip) {
		return MainMerge{}, nil
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
		return out, nil
	default:
		return MainMerge{}, fmt.Errorf("merging %s into %s: %w: %s", mainBranch, strs.ShortSHA(tip), err, stderrOf(err))
	}
	tree := fields[0]
	if !gitexec.IsObjectID(tree) {
		return MainMerge{}, fmt.Errorf("merging %s into %s: git wrote no tree (%q)", mainBranch, strs.ShortSHA(tip), tree)
	}
	commit, err := gitexec.HubOutput(ctx, repo, strings.NewReader(message), "-c", "user.name=apptown", "-c", "user.email=apptown@localhost",
		"commit-tree", tree, "-p", tip, "-p", main)
	if err != nil {
		return MainMerge{}, fmt.Errorf("committing the merge of %s into %s: %w: %s", mainBranch, strs.ShortSHA(tip), err, stderrOf(err))
	}
	out.Commit = strings.TrimSpace(string(commit))
	if !gitexec.IsObjectID(out.Commit) {
		return MainMerge{}, fmt.Errorf("committing the merge of %s into %s: git named no commit (%q)", mainBranch, strs.ShortSHA(tip), out.Commit)
	}
	return out, nil
}

func stderrOf(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(bytes.TrimSpace(exit.Stderr))
	}
	return ""
}

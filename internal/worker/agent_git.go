package worker

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec"
)

// An agent's repository (execution.AgentGit) is an ordinary Git repository
// at /work/.git, made with the workspace, that the task owns. The worker's
// own Git directory stays apart (gitDir) and its git is always given that
// directory, so it never reads the task's: no configuration, hook, filter or
// monitor the task writes there is run by the worker, and git never adds a
// .git to what it collects. What the task commits there reaches AppTown
// only as the tree it leaves, in the one commit the worker makes.

// agentGitFrom reads the agent's repository a workspace upload asks for;
// nil when it asks for none.
func (s *Server) agentGitFrom(q url.Values) (*execution.AgentGit, error) {
	branch, base, baseCommit := q.Get("agentBranch"), q.Get("agentBase"), q.Get("agentBaseCommit")
	if branch == "" {
		if base != "" || baseCommit != "" {
			return nil, fmt.Errorf("an agent's base needs its branch")
		}
		return nil, nil
	}
	out := &execution.AgentGit{Branch: branch, Base: base, BaseCommit: baseCommit}
	names := []string{branch}
	if base != "" || baseCommit != "" {
		if base == "" || !commitPattern.MatchString(baseCommit) || base == branch {
			return nil, fmt.Errorf("an agent's base needs its own branch name and commit")
		}
		names = append(names, base)
	}
	for _, name := range names {
		if gitexec.CheckBranchName(name) != nil {
			return nil, fmt.Errorf("%q is not a branch name", name)
		}
	}
	return out, nil
}

// seedAgentGit makes the agent's repository in tree/.git from the uploaded
// bundle, whose branch holds commit. staging is the upload's scratch folder.
// The tree already holds commit's files and the index is read from commit,
// so the agent starts clean.
func (s *Server) seedAgentGit(ctx context.Context, bundle, branch, commit, tree, staging string, a *execution.AgentGit) (string, error) {
	repo := filepath.Join(staging, "agent")
	steps := [][]string{
		{"init", "-q", repo},
		{"-C", repo, "fetch", "-q", "--no-tags", bundle, "refs/heads/" + branch + ":refs/heads/" + a.Branch},
		{"-C", repo, "symbolic-ref", "HEAD", "refs/heads/" + a.Branch},
		{"-C", repo, "config", "user.name", "apptown"},
		{"-C", repo, "config", "user.email", "apptown@localhost"},
		{"-C", repo, "read-tree", "HEAD"},
	}
	if a.Base != "" {
		// The base is named only when it is in the source's history: the
		// agent compares its work with what the change started from.
		steps = append(steps,
			[]string{"-C", repo, "merge-base", "--is-ancestor", a.BaseCommit, commit},
			[]string{"-C", repo, "update-ref", "refs/heads/" + a.Base, a.BaseCommit})
	}
	for _, args := range steps {
		if out, err := s.runGit(ctx, "", args...); err != nil {
			if out == "" && args[0] == "-C" {
				out = "git " + strings.Join(args[2:], " ") + " failed"
			}
			return out, err
		}
	}
	if head, err := s.runGit(ctx, "", "-C", repo, "rev-parse", "HEAD"); err != nil || head != commit {
		return "the agent's repository is not at the source commit", fmt.Errorf("agent repository at %q", head)
	}
	return "", os.Rename(filepath.Join(repo, ".git"), filepath.Join(tree, ".git"))
}

package verification

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
)

// StageCommittedTree extracts rev (a commit in repo) into a fresh directory
// under the forge's data directory, outside any writable workspace, so a
// writer cannot change the tree while it is being checked. One directory per
// invocation: two checks on the same change at once must not share it.
// Staging a commit that cannot be read is an error, never a
// quiet fallback to checking a live folder: repo must be a Git repository.
func StageCommittedTree(ctx context.Context, dataDir, repo, rev string) (string, func(), error) {
	noop := func() {}
	if !gitexec.IsRepo(repo) {
		return "", noop, fmt.Errorf("staging %s: %s is not a Git repository", rev, repo)
	}
	root := filepath.Join(dataDir, "staging")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", noop, fmt.Errorf("staging %s: %w", rev, err)
	}
	staged, err := os.MkdirTemp(root, "conformance-src-")
	if err != nil {
		return "", noop, fmt.Errorf("staging %s: %w", rev, err)
	}
	if err := gitexec.ExtractCommit(ctx, repo, rev, staged); err != nil {
		_ = os.RemoveAll(staged)
		return "", noop, fmt.Errorf("staging %s: %w", rev, err)
	}
	return staged, func() { _ = os.RemoveAll(staged) }, nil
}

// checked is the one commit a run's source gates check, resolved once: its
// ID, the repository that holds it, and its tree staged outside every
// folder. Staging, image selection, diffs and evidence all read these.
type checked struct {
	Src, Repo, Commit string
	// Candidate is the commit when it is a landing's pinned candidate, and
	// empty for any other run: runtime checks tell the two apart.
	Candidate string
}

// Selected is the one commit a run's gates read, resolved once: the
// repository that holds it and its full ID. Candidate is the commit when it
// is a landing's pinned candidate, and empty for any other run.
type Selected struct {
	Repo, Commit, Candidate string
}

// SelectCommit is the commit a run's gates read, for guidance checks and
// tests steps alike:
//   - a landing reads its pinned candidate, from the project's repository
//     that holds it (its run folder may not);
//   - any other run reads what the change's views read
//     (scm.ComparisonRepository): its branch in the project's repository
//     when the folder is behind work saved there (a chat's save or a task's
//     result the folder could not take) or absent, else the folder's HEAD.
//     A run without a change reads the same way from its own folder and
//     branch.
//
// Source that is missing or cannot be read is an error, never a check of
// the live folder, and never a pass.
func SelectCommit(ctx context.Context, db database.Queryer, dataDir string, run *workflow.Run, source workflow.Source) (Selected, error) {
	rev, pinned, err := checkedRevision(ctx, db, run.ID)
	if err != nil {
		return Selected{}, err
	}
	var s Selected
	project, ws := source.Repository(dataDir), source.Workspace(dataDir)
	switch {
	case pinned:
		s.Repo, s.Candidate = project, rev
	case source.RepoBacked() && ws != project:
		read, err := scm.ComparisonRepository(ctx, project, ws)
		if err != nil {
			return Selected{}, fmt.Errorf("reading the run's work: %w", err)
		}
		s.Repo = read
		if read != ws {
			rev = "refs/heads/" + source.Branch
		}
	default:
		s.Repo = ws
		if !gitexec.IsRepo(s.Repo) {
			return Selected{}, fmt.Errorf("a check reads a commit, and this run's folder has no repository")
		}
	}
	if s.Commit, err = gitexec.Output(ctx, s.Repo, "rev-parse", "--verify", rev+"^{commit}"); err != nil {
		if s.Repo == project && !pinned {
			return Selected{}, fmt.Errorf("reading the commit to check: the run's folder is absent or behind, and its branch %s is not in the project's repository: %w", source.Branch, err)
		}
		return Selected{}, fmt.Errorf("reading the commit to check: %w", err)
	}
	s.Commit = strings.TrimSpace(s.Commit)
	return s, nil
}

// checkedSource stages what a run's source gates check (SelectCommit)
// outside every folder.
func checkedSource(ctx context.Context, db database.Queryer, dataDir string, run *workflow.Run, source workflow.Source) (c checked, cleanup func(), err error) {
	noop := func() {}
	sel, err := SelectCommit(ctx, db, dataDir, run, source)
	if err != nil {
		return c, noop, err
	}
	c.Repo, c.Commit, c.Candidate = sel.Repo, sel.Commit, sel.Candidate
	c.Src, cleanup, err = StageCommittedTree(ctx, dataDir, c.Repo, c.Commit)
	return c, cleanup, err
}

// checkedRevision is the commit a run's source gates check: a landing's
// pinned candidate, otherwise the workspace's HEAD. pinned reports which.
func checkedRevision(ctx context.Context, db database.Queryer, runID ids.ID) (rev string, pinned bool, err error) {
	c, ok, err := workflow.LandingCandidateIn(ctx, db, runID)
	if err != nil || !ok {
		return "HEAD", false, err
	}
	return c.Candidate, true, nil
}

// landingRange names the commits a landing brings to the default branch,
// as a Git revision range: its candidate's commits not already on the
// target it was checked against (all of them for a repository's first
// history).
func landingRange(ctx context.Context, db database.Queryer, runID ids.ID) (string, error) {
	c, _, err := workflow.LandingCandidateIn(ctx, db, runID)
	if err != nil {
		return "", err
	}
	if c.Target == "" {
		return c.Candidate, nil
	}
	return c.Target + ".." + c.Candidate, nil
}

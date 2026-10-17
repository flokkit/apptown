package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/gitexec"
)

type CloneRequest struct {
	Repository, Workspace, Branch, DefaultBranch string
	RequireBranch                                bool // a recorded historical base must not invent a replacement branch
}
type WorkspaceResult struct {
	Path, Tip string
	Created   bool
}

// EnsureClone publishes a complete standalone clone. An existing workspace is
// validated, never reset, switched to another branch or converted in place.
func (g GitWorkspace) EnsureClone(ctx context.Context, in CloneRequest) (out WorkspaceResult, err error) {
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if in.Repository == "" || in.Workspace == "" || in.Branch == "" {
		return out, fmt.Errorf("workspace needs a repository, path and branch")
	}
	if err := gitexec.CheckBranchName(in.Branch); err != nil {
		return out, err
	}
	gitdir := filepath.Join(in.Workspace, ".git")
	if info, err := os.Lstat(gitdir); err == nil {
		if !info.IsDir() {
			return out, fmt.Errorf("workspace %s must have a self-contained .git directory; recreate or convert it offline", in.Workspace)
		}
		return g.existingClone(ctx, in.Workspace, in.Branch)
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	if _, err := os.Lstat(in.Workspace); err == nil {
		return out, fmt.Errorf("workspace %s exists without standalone Git metadata; inspect it before retrying", in.Workspace)
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	ref := "refs/heads/" + in.Branch
	probe := g.command(ctx, in.Repository, "show-ref", "--verify", "--quiet", ref)
	switch {
	case probe.Termination == execution.Exited && probe.ExitCode == 1:
		if in.RequireBranch {
			return out, fmt.Errorf("historical branch %q is missing; inspect before preparing its workspace", in.Branch)
		}
		if in.DefaultBranch == "" {
			in.DefaultBranch, err = g.git(ctx, in.Repository, "symbolic-ref", "--quiet", "--short", "HEAD")
			if err != nil {
				return out, err
			}
		}
		ref = "refs/heads/" + in.DefaultBranch
	case probe.Termination == execution.Exited && probe.ExitCode == 0:
	default:
		return out, fmt.Errorf("reading workspace branch: %w", gitOutcomeError(ctx, probe))
	}
	start, err := g.git(ctx, in.Repository, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return out, err
	}
	if err := os.MkdirAll(filepath.Dir(in.Workspace), 0755); err != nil {
		return out, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(in.Workspace), ".forge-preparing-")
	if err != nil {
		return out, err
	}
	defer func() {
		if cleanup := os.RemoveAll(staging); cleanup != nil {
			err = errors.Join(err, fmt.Errorf("remove owned staging workspace: %w", cleanup))
		}
	}()
	checkout := filepath.Join(staging, "checkout")
	// The clone's identity is written by clone itself (-c), before anything
	// is fetched or checked out, rather than by two later config commands.
	// -c adds to what a Git template copied in, so the clone takes no
	// template (--template= is none): a personal template's identity or
	// hooks would otherwise stay beside the forge's, and CheckFolder refuses
	// both. So a folder has no sample hooks, info/exclude or description.
	if _, err := g.git(ctx, "", "clone", "--quiet", "--no-hardlinks", "--no-checkout", "--template=", "-c", "user.name="+gitexec.FolderUserName, "-c", "user.email="+gitexec.FolderUserEmail, in.Repository, checkout); err != nil {
		return out, err
	}
	for _, args := range [][]string{{"checkout", "--quiet", "-B", in.Branch, start}, {"remote", "remove", "origin"}} {
		if _, err := g.git(ctx, checkout, args...); err != nil {
			return out, err
		}
	}
	ready, err := g.existingClone(ctx, checkout, in.Branch)
	if err != nil {
		return out, err
	}
	if ready.Tip != start {
		return out, fmt.Errorf("prepared workspace changed before publication")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := os.Rename(checkout, in.Workspace); err != nil {
		return out, fmt.Errorf("publishing prepared workspace: %w", err)
	}
	return WorkspaceResult{Path: in.Workspace, Tip: ready.Tip, Created: true}, nil
}
func (g GitWorkspace) existingClone(ctx context.Context, path, branch string) (WorkspaceResult, error) {
	// One git start answers both questions: HEAD's commit, then the ref HEAD
	// names ("HEAD" when detached). On the branch, HEAD's commit is the
	// branch's. The closing "--" makes git read both as revisions, never
	// as paths; git echoes it.
	out, err := g.git(ctx, path, "rev-parse", "HEAD^{commit}", "--symbolic-full-name", "HEAD", "--")
	if err != nil {
		return WorkspaceResult{}, err
	}
	answer := strings.Split(out, "\n")
	if len(answer) != 3 || !gitexec.IsObjectID(answer[0]) || answer[2] != "--" {
		return WorkspaceResult{}, fmt.Errorf("reading the workspace's branch: unexpected answer %q", out)
	}
	if current := answer[1]; current != "refs/heads/"+branch {
		return WorkspaceResult{}, fmt.Errorf("workspace is on %q, expected %q; inspect without discarding its work", strings.TrimPrefix(current, "refs/heads/"), branch)
	}
	return WorkspaceResult{Path: path, Tip: answer[0]}, nil
}

// ErrFolderDiverged: a change's folder and its branch as published have both
// moved. The folder's branch was not moved; the published branch is fetched
// into it as PublishedRef, for whoever works the folder to merge.
var ErrFolderDiverged = fault.New(fault.Conflict, "the change's folder and its branch have both moved; merge the branch into the folder (the chat can) and try again")

// PublishedRef is where a diverged folder finds its branch as published.
const PublishedRef = "refs/forge/published"

// leftoverMessage names the commit of what a change's folder had not
// committed when a new holder took it.
const leftoverMessage = "forge: work left in the change's folder by its previous worker"

// BringToBranch readies a change's folder for a new holder, before it works
// there. What
// the folder has not committed is committed first, as the previous worker's
// (staged as StageAll stages: never .forge runtime files), so it is neither
// discarded nor mistaken for the new holder's work. The folder is then
// fast-forwarded to its branch as published when it has nothing of its own;
// one that already contains the branch, or a branch never published, is left
// as it is. When both have moved, the folder's branch stays where it is, the
// published branch is fetched to PublishedRef, and ErrFolderDiverged is
// returned. The folder is never merged or reset here.
func (g GitWorkspace) BringToBranch(ctx context.Context, workspace, repository, branch string) error {
	// A missing branch answers with exit 1 under --quiet; anything else is a
	// failure to read.
	probe := g.command(ctx, repository, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if probe.Termination == execution.Exited && probe.ExitCode == 1 {
		return g.commitLeftovers(ctx, workspace) // never published
	}
	if err := gitOutcomeError(ctx, probe); err != nil {
		return fmt.Errorf("reading the published branch: %w", err)
	}
	published := strings.TrimSpace(string(probe.Output))
	if err := g.commitLeftovers(ctx, workspace); err != nil {
		return err
	}
	local, err := g.git(ctx, workspace, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if local == published {
		return nil
	}
	if _, err := g.git(ctx, workspace, "fetch", "--quiet", "--no-tags", repository, "refs/heads/"+branch); err != nil {
		return err
	}
	if g.command(ctx, workspace, "merge-base", "--is-ancestor", published, local).ExitCode == 0 {
		return nil // the folder already contains the branch
	}
	if g.command(ctx, workspace, "merge-base", "--is-ancestor", local, published).ExitCode != 0 {
		if _, err := g.git(ctx, workspace, "update-ref", PublishedRef, published); err != nil {
			return err
		}
		return ErrFolderDiverged
	}
	if _, err := g.git(ctx, workspace, "merge", "--ff-only", "--quiet", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("bringing the change's folder to its branch: %w", err)
	}
	return nil
}

func (g GitWorkspace) commitLeftovers(ctx context.Context, workspace string) error {
	if err := scm.StageAll(ctx, workspace); err != nil {
		return err
	}
	if staged := g.command(ctx, workspace, "diff", "--cached", "--quiet"); staged.Termination == execution.Exited && staged.ExitCode == 0 {
		return nil
	} else if staged.ExitCode != 1 {
		return fmt.Errorf("reading the change's folder: %w", gitOutcomeError(ctx, staged))
	}
	_, err := g.git(ctx, workspace, "-c", "user.name=forge", "-c", "user.email=forge@localhost", "commit", "--quiet", "-m", leftoverMessage)
	return err
}

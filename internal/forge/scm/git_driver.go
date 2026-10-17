package scm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/gitlab"
	"flokkit.io/forge/internal/strs"
	"flokkit.io/forge/internal/trailer"
)

// GitDrivers creates one driver per merge or inspection. Provider credentials
// are frozen for that operation, never shared across unrelated merges.
type GitDrivers struct {
	Runner   execution.Runner
	DataDir  string
	Provider func(context.Context) (*gitlab.Client, error)
	// ChangeURL is a change's page on this forge, "" when the forge has no
	// public address; merge commits and merge requests link to it.
	ChangeURL func(changeID ids.ID) string
}

func (d GitDrivers) Driver() Driver {
	return &gitDriver{runner: d.Runner, dataDir: d.DataDir, provider: d.Provider, changeURL: d.ChangeURL}
}

// This adapter knows Git and the provider protocol, not Work rows or SQL.
// One driver instance freezes provider credentials for one operation.
type gitDriver struct {
	runner      execution.Runner
	dataDir     string
	provider    func(context.Context) (*gitlab.Client, error)
	changeURL   func(ids.ID) string
	once        sync.Once
	client      *gitlab.Client
	env         []string
	providerErr error
}

func (d *gitDriver) git(ctx context.Context, repo string, env []string, args ...string) (string, error) {
	out := d.runner.Run(ctx, execution.Local, execution.Request{Workspace: repo, Argv: append([]string{"git"}, args...), Env: env, Timeout: 10 * time.Minute})
	if ctx.Err() != nil {
		return strings.TrimSpace(string(out.Output)), ctx.Err()
	}
	return strings.TrimSpace(string(out.Output)), out.RunErr
}
func (d *gitDriver) ancestor(ctx context.Context, repo, from, to string) (bool, error) {
	out := d.runner.Run(ctx, execution.Local, execution.Request{Workspace: repo, Argv: []string{"git", "merge-base", "--is-ancestor", from, to}, Timeout: 10 * time.Minute})
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if out.Termination == execution.Exited && out.ExitCode == 1 {
		return false, nil
	}
	return out.RunErr == nil, out.RunErr
}
func (d *gitDriver) revision(ctx context.Context, repo, ref string) (string, error) {
	out, err := d.git(ctx, repo, nil, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil || out == "" {
		return "", fmt.Errorf("reading committed revision %s: %w", ref, err)
	}
	return out, nil
}

// pageOf is the merge's change page, "" when unknown.
func (d *gitDriver) pageOf(source Source) string {
	if d.changeURL == nil || source.Attribution == nil || source.Attribution.ChangeID.IsZero() {
		return ""
	}
	return d.changeURL(source.Attribution.ChangeID)
}
func (d *gitDriver) providerClient(ctx context.Context) (*gitlab.Client, []string, error) {
	d.once.Do(func() {
		if d.provider == nil {
			d.providerErr = fmt.Errorf("merge provider is unavailable")
			return
		}
		d.client, d.providerErr = d.provider(ctx)
		if d.providerErr == nil {
			d.env, d.providerErr = d.client.AskpassEnv(d.dataDir)
		}
	})
	return d.client, d.env, d.providerErr
}
func (d *gitDriver) Prepare(ctx context.Context, source Source, previous string) (Prepared, error) {
	repo, branch := source.Repository, source.Branch
	checkedOut, err := d.git(ctx, repo, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return Prepared{}, fmt.Errorf("reading the project checkout: %w", err)
	}
	if checkedOut != source.DefaultBranch {
		return Prepared{}, fmt.Errorf("project checkout is on %q, expected %q", checkedOut, source.DefaultBranch)
	}
	if source.MR.IID != 0 {
		return d.prepareMR(ctx, source)
	}
	head, err := d.revision(ctx, repo, "refs/heads/"+source.DefaultBranch)
	if err != nil {
		return Prepared{}, err
	}
	out := d.runner.Run(ctx, execution.Local, execution.Request{Workspace: repo, Argv: []string{"git", "show-ref", "--verify", "--quiet", "refs/heads/" + branch}, Timeout: 10 * time.Minute})
	if ctx.Err() != nil {
		return Prepared{}, ctx.Err()
	}
	if out.Termination == execution.Exited && out.ExitCode == 1 {
		return Prepared{Kind: Empty}, nil
	}
	if out.RunErr != nil {
		return Prepared{}, fmt.Errorf("reading workflow branch: %w", out.RunErr)
	}
	merged, err := d.ancestor(ctx, repo, "refs/heads/"+branch, head)
	if err != nil {
		return Prepared{}, err
	}
	if merged {
		reuse := false
		if previous != "" {
			reuse, err = d.ancestor(ctx, repo, "refs/heads/"+branch, previous)
			if err != nil {
				return Prepared{}, err
			}
		}
		if reuse {
			head = previous
		} else if err := d.checkedTreeLanded(ctx, source, head); err != nil {
			return Prepared{}, err
		}
		return Prepared{Kind: Existing, Result: head, ReuseRecorded: reuse}, nil
	}
	drift, err := Drift(ctx, d.runner, repo)
	if err != nil {
		return Prepared{}, err
	}
	if len(drift) > 0 {
		diff, err := d.git(ctx, repo, nil, "diff")
		if err != nil {
			return Prepared{}, err
		}
		return Prepared{Kind: Blocked, Drift: drift, DriftDiff: strs.Truncate(diff, 8192)}, nil
	}
	if err := pinnedLanding(ctx, source, head); err != nil {
		return Prepared{}, err
	}
	return Prepared{Kind: LocalMerge, Expected: head}, nil
}

// checkedTreeLanded is the candidate rule for a branch already on main
// that this landing has not recorded a result for: main's head becomes the
// landing's result, so it must hold the tree the landing checked. An empty
// change's branch is always on main; without this, a main that moved after
// the checks would be recorded as landed (and an adoption applied on it).
// Trees, not commits: a change someone merged by fast-forward, or by a merge
// commit that added nothing else, still settles.
func (d *gitDriver) checkedTreeLanded(ctx context.Context, source Source, head string) error {
	if source.Candidate == "" {
		return nil
	}
	if tip := Ref(ctx, source.Repository, "refs/heads/"+source.Branch); tip != source.Candidate {
		return &LandingMoved{What: "branch", Pinned: source.Candidate, Found: tip}
	}
	tree := func(commit string) (string, error) {
		out, err := d.git(ctx, source.Repository, nil, "rev-parse", "--verify", commit+"^{tree}")
		if err != nil || out == "" {
			return "", fmt.Errorf("reading the tree of %s: %w", strs.ShortSHA(commit), err)
		}
		return out, nil
	}
	landed, err := tree(head)
	if err != nil {
		return err
	}
	checked, err := tree(source.Candidate)
	if err != nil {
		return err
	}
	if landed != checked {
		return &LandingMoved{What: "main", Pinned: source.Target, Found: head}
	}
	return nil
}

// pinnedLanding refuses a local merge unless the branch is at the checked
// candidate and the default branch at the target it was caught up with. The
// candidate contains the target, so the merge's tree is the candidate's. The
// merge runs under the repository lock, so neither can move before it.
func pinnedLanding(ctx context.Context, source Source, head string) error {
	if source.Candidate == "" {
		return nil
	}
	if tip := Ref(ctx, source.Repository, "refs/heads/"+source.Branch); tip != source.Candidate {
		return &LandingMoved{What: "branch", Pinned: source.Candidate, Found: tip}
	}
	if head != source.Target {
		return &LandingMoved{What: "main", Pinned: source.Target, Found: head}
	}
	if source.Target != "" && !ancestor(ctx, source.Repository, source.Target, source.Candidate) {
		return fmt.Errorf("the landing's candidate %s does not contain the default branch it was checked against (%s); the landing needs a catch-up step before its checks", strs.ShortSHA(source.Candidate), strs.ShortSHA(source.Target))
	}
	return nil
}
func (d *gitDriver) Execute(ctx context.Context, source Source, plan Prepared, operation string) (Execution, error) {
	if plan.Kind == ProviderMerge {
		return d.executeMR(ctx, source, plan)
	}
	if plan.Kind != LocalMerge {
		return Execution{}, fmt.Errorf("invalid Git effect plan")
	}
	message := mergeMessage(source, operation, d.pageOf(source))
	_, mergeErr := d.git(ctx, source.Repository, nil, "merge", "--no-ff", source.Branch, "-m", message)
	if mergeErr != nil {
		current, err := d.revision(ctx, source.Repository, "refs/heads/"+source.DefaultBranch)
		if err != nil {
			return Execution{}, err
		}
		if current != plan.Expected {
			return Execution{}, fmt.Errorf("merge failed after the default branch moved: %w", mergeErr)
		}
		files, err := d.git(ctx, source.Repository, nil, "diff", "--name-only", "--diff-filter=U")
		if err != nil {
			return Execution{}, err
		}
		conflicts := strings.FieldsFunc(files, func(r rune) bool { return r == '\n' })
		result := Execution{Unchanged: true, Conflicts: conflicts}
		if len(conflicts) == 0 {
			return result, fmt.Errorf("merging the workflow branch: %w", mergeErr)
		}
		// Prepare refused pre-existing drift. These conflicts belong to this
		// attempted merge; abort only that merge, never reset a caller's edits.
		if _, err := d.git(ctx, source.Repository, nil, "merge", "--abort"); err != nil {
			return Execution{}, fmt.Errorf("aborting conflicted merge: %w", err)
		}
		return result, nil
	}
	sha, err := d.revision(ctx, source.Repository, "refs/heads/"+source.DefaultBranch)
	return Execution{Result: sha}, err
}
func (d *gitDriver) Inspect(ctx context.Context, source Source, record Record) (Inspection, error) {
	head, err := d.revision(ctx, source.Repository, "refs/heads/"+source.DefaultBranch)
	if err != nil {
		return Inspection{}, err
	}
	found, err := d.mergeCommit(ctx, source.Repository, source.DefaultBranch, record.Operation)
	if err != nil {
		return Inspection{}, err
	}
	if found != "" {
		return Inspection{Kind: Found, Result: found}, nil
	}
	if head == record.Expected {
		return Inspection{Kind: Unchanged}, nil
	}
	return Inspection{Kind: Unresolved}, nil
}
func (d *gitDriver) mergeCommit(ctx context.Context, repo, branch, operation string) (string, error) {
	out, err := d.git(ctx, repo, nil, "log", "--format=%H", "-n", "1", "--extended-regexp", "--grep=^"+trailer.Operation+": "+regexp.QuoteMeta(operation)+"$", "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("inspecting merge operation: %w", err)
	}
	for _, sha := range strings.Fields(out) {
		body, err := d.git(ctx, repo, nil, "show", "-s", "--format=%B", sha)
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(body, "\n") {
			if line == trailer.Operation+": "+operation {
				return sha, nil
			}
		}
	}
	return "", nil
}

// prepareMR readies a landing's merge through its recorded merge request.
// What merges must be exactly the checked candidate, so the provider must
// guarantee it (design/architecture/scm-domain.md): read now, at the act, the project must
// merge fast-forward or semi-linear (either refuses a branch not up to date
// with its target, so the result is the candidate's tree), the request must
// not squash, the target must still be where the candidate was checked, and
// the request's head must be the candidate, pushed as that exact commit.
func (d *gitDriver) prepareMR(ctx context.Context, source Source) (Prepared, error) {
	if source.Candidate == "" {
		return Prepared{}, fmt.Errorf("this landing has no pinned candidate; it cannot merge")
	}
	gl, env, err := d.providerClient(ctx)
	if err != nil {
		return Prepared{}, err
	}
	tip := source.Candidate
	current, err := gl.GetMR(ctx, source.MR.Project, source.MR.IID)
	if err != nil {
		return Prepared{}, err
	}
	if current.SourceBranch != source.Branch || current.TargetBranch != source.DefaultBranch {
		return Prepared{}, fmt.Errorf("recorded merge request names different branches")
	}
	switch current.State {
	case "merged":
		if current.SHA != tip {
			return Prepared{}, fmt.Errorf("this merge request landed a different commit than the checked candidate; create a new merge request")
		}
		sha, err := d.confirmMR(ctx, source, tip, current)
		return Prepared{Kind: Existing, Result: sha}, err
	case "opened":
		head, err := d.revision(ctx, source.Repository, "refs/heads/"+source.DefaultBranch)
		if err != nil {
			return Prepared{}, err
		}
		if err := pinnedLanding(ctx, source, head); err != nil {
			return Prepared{}, err
		}
	default:
		return Prepared{}, fmt.Errorf("recorded merge request is %q; it is not ready to merge", current.State)
	}
	settings, err := gl.MergeSettings(ctx, source.MR.Project)
	if err != nil {
		return Prepared{}, err
	}
	if err := checkedMergeSettings(settings, current); err != nil {
		return Prepared{}, err
	}
	head, err := gl.BranchHead(ctx, source.MR.Project, source.DefaultBranch)
	if err != nil {
		return Prepared{}, err
	}
	if head != source.Target {
		return Prepared{}, &LandingMoved{What: "main", Pinned: source.Target, Found: head}
	}
	// The request's branch is set to exactly the candidate; a branch with
	// commits the forge did not check refuses the push instead of losing
	// them.
	out, err := d.git(ctx, source.Repository, env, "push", "origin", tip+":refs/heads/"+source.Branch)
	if err != nil {
		return Prepared{}, fmt.Errorf("publishing the checked candidate to the merge request: %w: %s", err, strs.Truncate(out, 200))
	}
	current, err = gl.GetMR(ctx, source.MR.Project, source.MR.IID)
	if err != nil {
		return Prepared{}, err
	}
	if current.SHA != tip || current.State != "opened" || current.SourceBranch != source.Branch || current.TargetBranch != source.DefaultBranch || current.Squash {
		return Prepared{}, fmt.Errorf("merge request changed or has not observed the checked candidate; retry")
	}
	return Prepared{Kind: ProviderMerge, Expected: tip}, nil
}

// ErrMergeRequestSettings: the provider cannot guarantee that merging the
// request lands exactly the checked candidate; the landing is refused with
// the setting to change, or it lands directly.
var ErrMergeRequestSettings = errors.New("this merge request cannot land the checked candidate exactly")

// checkedMergeSettings refuses provider settings under which a merge could
// differ from the checked candidate: a plain merge commit (combines the
// candidate with anything that landed meanwhile) or a squash (lands a new
// commit instead of the checked one).
func checkedMergeSettings(settings gitlab.MergeSettings, mr *gitlab.MergeRequest) error {
	if settings.MergeMethod != "ff" && settings.MergeMethod != "rebase_merge" {
		return fmt.Errorf("%w: the GitLab project merges with plain merge commits (%q); set its merge method to fast-forward, or merge commit with semi-linear history, or land directly", ErrMergeRequestSettings, settings.MergeMethod)
	}
	if settings.SquashOption == "always" || mr.Squash {
		return fmt.Errorf("%w: the merge request would squash its commits; turn squashing off for it (the project's squash option must not be \"always\"), or land directly", ErrMergeRequestSettings)
	}
	// Rebasing on the provider lands rebased copies of the candidate's
	// commits onto whatever the target is by then, not the candidate.
	if settings.AutomaticRebase == nil {
		return fmt.Errorf("%w: GitLab does not say whether this project rebases automatically before merging; land directly", ErrMergeRequestSettings)
	}
	if *settings.AutomaticRebase {
		return fmt.Errorf("%w: the GitLab project rebases automatically before merging; turn that off, or land directly", ErrMergeRequestSettings)
	}
	return nil
}

func (d *gitDriver) executeMR(ctx context.Context, source Source, plan Prepared) (Execution, error) {
	gl, _, err := d.providerClient(ctx)
	if err != nil {
		return Execution{}, err
	}
	// The description says what admitted this merge before GitLab shows it
	// merged. If GitLab will not take it, nothing merges: this landing
	// already needs the same API for every call below, and the attempt is
	// retried whole. A request someone else merged is never described as
	// the forge's merge: prepareMR settles it without coming here.
	if source.Attribution != nil {
		description := MergeRequestDescription(*source.Attribution, d.pageOf(source))
		if err := gl.UpdateMRDescription(ctx, source.MR.Project, source.MR.IID, description); err != nil {
			return Execution{}, fmt.Errorf("could not record the approvals on merge request !%d, so it was not merged; retry: %w", source.MR.IID, err)
		}
	}
	message := mergeMessage(source, "", d.pageOf(source))
	if err := gl.MergeMR(ctx, source.MR.Project, source.MR.IID, plan.Expected, message); err != nil {
		return Execution{}, err
	}
	current, err := gl.GetMR(ctx, source.MR.Project, source.MR.IID)
	if err != nil {
		return Execution{}, err
	}
	sha, err := d.confirmMR(ctx, source, plan.Expected, current)
	return Execution{Result: sha}, err
}

// confirmMR confirms that what the provider merged is exactly the checked
// landing, and only then moves the forge's default branch to it. Allowed are
// the candidate itself (a fast-forward), or one merge commit whose parents
// are the pinned target and the candidate (semi-linear), with the
// candidate's tree. Anything else (a squash, a rebase, a merge with other
// history) is refused: the provider may have landed it, but it is not this
// checked landing, and the forge's main does not follow. The forge's main
// moves to the confirmed commit, never to later provider history.
func (d *gitDriver) confirmMR(ctx context.Context, source Source, tip string, current *gitlab.MergeRequest) (string, error) {
	if current.State != "merged" || current.SHA != tip || current.SourceBranch != source.Branch || current.TargetBranch != source.DefaultBranch {
		return "", fmt.Errorf("merge request has not confirmed the selected source as merged; retry")
	}
	if current.SquashCommitSHA != "" {
		return "", fmt.Errorf("the provider squashed this merge request (%s); it is not the checked candidate, and main was not moved here", strs.ShortSHA(current.SquashCommitSHA))
	}
	result := current.MergeCommitSHA
	if result == "" {
		result = current.SHA
	}
	_, env, err := d.providerClient(ctx)
	if err != nil {
		return "", err
	}
	out, err := d.git(ctx, source.Repository, env, "fetch", "origin", source.DefaultBranch)
	if err != nil {
		return "", fmt.Errorf("merge request merged but local fetch failed: %w: %s", err, strs.Truncate(out, 200))
	}
	ancestor, err := d.ancestor(ctx, source.Repository, result, "FETCH_HEAD")
	if err != nil {
		return "", err
	}
	if !ancestor {
		return "", fmt.Errorf("provider merge result is not on the fetched default branch")
	}
	if result != source.Candidate {
		parents, err := d.git(ctx, source.Repository, nil, "rev-list", "--parents", "-n", "1", result)
		if err != nil {
			return "", fmt.Errorf("reading the provider's merge: %w", err)
		}
		if want := result + " " + source.Target + " " + source.Candidate; parents != want {
			return "", fmt.Errorf("the provider's merge %s is not the checked landing (a merge of %s into %s); main was not moved here", strs.ShortSHA(result), strs.ShortSHA(source.Candidate), strs.ShortSHA(source.Target))
		}
	}
	landed, err := d.git(ctx, source.Repository, nil, "rev-parse", "--verify", result+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("reading the merged tree: %w", err)
	}
	checked, err := d.git(ctx, source.Repository, nil, "rev-parse", "--verify", source.Candidate+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("reading the candidate's tree: %w", err)
	}
	if landed != checked {
		return "", fmt.Errorf("the provider merged a tree other than the checked candidate's (%s, checked %s); main was not moved here", strs.ShortSHA(landed), strs.ShortSHA(checked))
	}
	out, err = d.git(ctx, source.Repository, nil, "merge", "--ff-only", result)
	if err != nil {
		return "", fmt.Errorf("merge request merged but local default branch diverged: %w: %s", err, strs.Truncate(out, 200))
	}
	return result, nil
}

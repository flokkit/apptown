package verification

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/strs"
)

// Changes judges a committed Change against its declared checks.
// It owns baseline memoization and receives the same scanner/evaluator owners
// as the landing gate. It has no transport or App back-reference.
type Changes struct {
	db          *database.DB
	adoption    *adoption.Service
	conformance *conformance.Service
	scanners    *Scanners
	evaluator   *Evaluator
	dataDir     string
	workerChats WorkerChats
	baseline    *baselineCache
	clock       func() time.Time
}

// WorkerChats are the chats on the worker a check saves first
// (changeconsole.Saves).
type WorkerChats interface {
	// HeldBy names the chat on the worker that holds ch; "" when none does.
	HeldBy(ctx context.Context, ch *workflow.Change) (string, error)
	// SaveFirst saves the chat holder names.
	SaveFirst(ctx context.Context, holder string) (bool, error)
}

// Check runs a change's committed tree through the same gates the
// landing will apply (guidance sh + tool checks and the source scanners)
// and reports raw. The domain command behind `forge check` on HTTP and
// run_change_checks on MCP.
//
// sel is the commit the caller asks about (CheckedCommit): a named commit
// (the CLI sends its folder's HEAD), the change's published branch tip, or,
// asking for neither, the change's latest commit on the forge. A commit the
// forge cannot answer for is refused, so an older commit's result is never
// answered for it. A check publishes and saves nothing: its answer is
// feedback (feedbackOnly), never a gate's evidence; the landing checks again.
// A commit the forge has only as a step's work in progress, pushed to its
// scratch ref (scm.ScratchRef), is checked as any other and marked scratch.
//
// A change a chat on the worker holds is checked as saved: the chat saves
// first, and a save refused or not confirmed refuses the check. An explicit
// request for the published tip saves nothing: it asks about what is
// already on the branch, and a check never publishes.
func (v *Changes) Check(ctx context.Context, ch *workflow.Change, sel Selection) (map[string]any, error) {
	if err := v.saveWorkerChat(ctx, ch, sel); err != nil {
		return nil, err
	}
	id := ch.ID
	// What is checked is settled first: a commit the forge cannot answer
	// for is refused before anything runs.
	read, headSHA, scratch, err := v.checkedCommit(ctx, ch, sel)
	if err != nil {
		return nil, err
	}

	// The checks of the revision the change's own run reads (§4.1): the
	// agent's loop and the gate agree.
	var checks []conformance.DeclaredCheck
	adoption := changeAdoption(ctx, v.db, ch)
	if adoption != nil {
		checks, err = ChecksOfReceipt(ctx, v.adoption, adoption)
	} else {
		checks, err = v.conformance.AdoptedChecks(ctx, ch.ProjectID)
	}
	if err != nil {
		return nil, err
	}
	src, cleanup, err := StageCommittedTree(ctx, v.dataDir, read, headSHA)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	results, findings, shErrors := v.evaluator.runShChecks(ctx, checks, src, fmt.Sprintf("forge-check-%d", id))

	// tool checks (design/subsystems/conformance.md §13.6): the agent sees the real tool
	// findings — SAST issues, framework self-check warnings — in its own loop.
	// The exec-image tier builds the app image once, content-addressed by the
	// commit, and the land gate reuses it — so running it here costs a shared
	// build, not a duplicate one, and it never costs a wasted land spin.
	toolResults, toolFindings, toolErrors := v.scanners.RunToolChecks(ctx, checks,
		ToolContext{Context: ctx, ProjectID: ch.ProjectID, SHA: headSHA, BuildDir: src, AllowBuild: true},
		fmt.Sprintf("forge-check-%d", id))
	results = append(results, toolResults...)
	findings = append(findings, toolFindings...)
	shErrors += toolErrors

	// Reflect the land gate's SOURCE scanners (osv/gitleaks/semgrep) too, so the
	// agent sees dependency CVEs and SAST findings in its own loop instead of
	// after handoff when the land sticks (design/subsystems/conformance.md §13.6). The land
	// fails a source scan on ANY actionable finding, so scan findings count as
	// failing regardless of severity — distinct from the guidance/tool findings
	// which gate at failOn.
	forkBase := strs.FirstNonBlank(v.forkPointSHA(ctx, ch.ProjectID, ch.Branch), ch.BaseSHA)
	scanResults, scanFindings, scanErrs := v.scanners.RunSourceScans(ctx, src, fmt.Sprintf("forge-check-%d", id), read, forkBase)
	results = append(results, scanResults...)
	shErrors += scanErrs
	// And the landing's image scan point (trivy by default), on the image
	// of this commit, judged as the scan-image step judges: its findings
	// at or above high that this change introduced (soak finding 8: an
	// image finding first seen at the landing needs a person to send the
	// change back). Feedback like the rest; the landing scans again.
	imageResults, imageFindings, imageErrs := v.scanners.RunImageScans(ctx, ch.ProjectID, src, headSHA, read, forkBase, fmt.Sprintf("forge-check-%d", id))
	results = append(results, imageResults...)
	scanFindings = append(scanFindings, imageFindings...)
	shErrors += imageErrs

	// The baseline, as the gate applies it: a finding that already fails
	// on the tree this change forked from is marked and does not count.
	baseSHA := strs.FirstNonBlank(ch.BaseSHA, v.forkPointSHA(ctx, ch.ProjectID, ch.Branch))
	base := v.baselineFor(ctx, ch.ProjectID, checks, baseSHA, fmt.Sprintf("forge-check-%d", id))
	// A check its caller stopped part-way has no answer: a cut-short phase
	// reads as nothing found, never as a pass.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	own, already, undecided := classifyAgainstBase(results, findings, checks, base, workflow.AcceptanceScopeOf(ch))

	// Gate rule mirrors the step: deterministic (sh + tool) findings at/above
	// failOn block, plus ANY source-scan finding. Reported raw (pre-disposition)
	// on purpose — the agent should FIX, not lean on a human's exception.
	const failOn = "high"
	failing := len(scanFindings)
	for _, f := range own {
		if check.SeverityRank(f.Severity) >= check.SeverityRank(failOn) {
			failing++
		}
	}
	out := map[string]any{
		// A check that could not run is not a pass.
		"ok":          failing == 0 && shErrors == 0,
		"failing":     failing,
		"preexisting": len(already),
		"undecided":   len(undecided),
		"checkErrors": shErrors,
		"failOn":      failOn,
		"checks":      results,
		"commit":      headSHA, // what was checked
		// Never evidence: nothing of this answer is kept, and no gate
		// reads it. The landing runs the checks again on what it lands.
		"feedbackOnly": true,
	}
	if scratch {
		out["scratch"] = true
	}
	if adoption != nil {
		out["adoption"] = map[string]any{"revision": adoption.Revision, "digest": adoption.Digest}
	}
	var notes []string
	if len(already) > 0 {
		notes = append(notes, fmt.Sprintf("%d finding(s) already fail on main and are not this change's to fix — marked preexisting, they do not block; leave them and mention them in your report", len(already)))
	}
	if len(undecided) > 0 {
		notes = append(notes, fmt.Sprintf("%d finding(s) fail on main too and the check cannot say whether this change made it worse — marked undecided, they do not block; do not widen this change to fix them, the reviewer judges the diff", len(undecided)))
	}
	if scratch {
		notes = append(notes, fmt.Sprintf("commit %s is work in progress the step sent for checking, not yet the change's: this result is feedback, and the landing checks the published commit again", headSHA))
	}
	if len(notes) > 0 {
		out["note"] = strings.Join(notes, ". ")
	}
	return out, nil
}

// saveWorkerChat saves the chat on the worker that holds ch, unless the
// published tip is asked for.
func (v *Changes) saveWorkerChat(ctx context.Context, ch *workflow.Change, sel Selection) error {
	if sel.Published || v.workerChats == nil {
		return nil
	}
	holder, err := v.workerChats.HeldBy(ctx, ch)
	if err != nil || holder == "" {
		return err
	}
	if _, err := v.workerChats.SaveFirst(ctx, holder); err != nil {
		return fault.Wrap(fault.Conflict, fmt.Errorf("change #%d was not checked: %w", ch.ID, err))
	}
	return nil
}

// Selection is which commit of a change a check reads, as its caller asked.
// Its zero value asks for neither a commit nor the published tip: the
// change's latest commit on the forge, the one its views read. MCP's
// run_change_checks, the CLI outside a Git repository, and older callers
// send that, and it keeps its meaning.
type Selection struct {
	// Commit names one commit: the CLI sends its folder's HEAD.
	Commit string
	// Published asks for the tip of the change's branch in the project's
	// repository: what has been published, whatever the folder holds.
	Published bool
}

// CheckedCommit selects the repository and commit a check of ch reads.
//
// A named commit must be a full object id (refused before git sees it) and
// the change's own work on the forge: in the history of the change's
// folder's HEAD (the local path, where the agent commits in place), of
// the change's branch in the project's hub (published), or of one of the
// change's scratch refs there (a step on a worker pushed it for checking:
// scm.ScratchRef). Anything else, including a commit only on a
// sibling's branch, is a CommitNotOnForgeError. Published is the tip of the
// change's branch in the hub, read once: that commit is checked, never the
// folder's, and a branch the hub does not have is a NothingPublishedError.
// A selection naming a commit and asking for the published tip is refused
// as invalid. Neither is the change's latest commit on the forge: the
// folder's HEAD, or the published branch when the folder is absent or
// behind work saved there.
func (v *Changes) CheckedCommit(ctx context.Context, ch *workflow.Change, sel Selection) (repo, sha string, err error) {
	repo, sha, _, err = v.checkedCommit(ctx, ch, sel)
	return repo, sha, err
}

// checkedCommit is CheckedCommit, saying too whether the commit is the
// change's only as a step's scratch work.
func (v *Changes) checkedCommit(ctx context.Context, ch *workflow.Change, sel Selection) (repo, sha string, scratch bool, err error) {
	p, err := project.ByIDIn(ctx, v.db, ch.ProjectID)
	if err != nil {
		return "", "", false, err
	}
	hub := filepath.Join(v.dataDir, "workspaces", "project-"+p.Slug)
	ws := filepath.Join(v.dataDir, "workspaces", fmt.Sprintf("change-%d", ch.ID))
	commit := sel.Commit
	if sel.Published {
		if commit != "" {
			return "", "", false, fault.Wrap(fault.Invalid, fmt.Errorf("ask for a named commit or the published tip, not both"))
		}
		repo, sha, err := publishedTip(ctx, hub, ch)
		return repo, sha, false, err
	}
	if commit != "" {
		if !gitexec.IsObjectID(commit) {
			return "", "", false, fault.Wrap(fault.Invalid, fmt.Errorf("%q is not a full commit id (40 or 64 lowercase hex digits, as `git rev-parse HEAD` prints)", commit))
		}
		// History, not mere presence: a folder cloned from the hub holds
		// its siblings' commits too, and they are not this change's work.
		if gitexec.IsRepo(ws) && within(ctx, ws, commit, "HEAD") {
			return ws, commit, false, nil
		}
		if ch.Branch != "" && gitexec.IsRepo(hub) && within(ctx, hub, commit, "refs/heads/"+ch.Branch) {
			return hub, commit, false, nil
		}
		if gitexec.IsRepo(hub) && withinScratch(ctx, hub, ch.ID, commit) {
			return hub, commit, true, nil
		}
		return "", "", false, &CommitNotOnForgeError{Commit: commit}
	}
	// The commit the change's views read: the folder's, or its branch when
	// the folder is behind work saved there (a chat on a worker).
	read, err := scm.ComparisonRepository(ctx, hub, ws)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", false, &noWorkspaceError{ch.ID} // a folder with no repository has no commit to check
	}
	if err != nil {
		return "", "", false, err // a damaged folder is an error, never a fall-back
	}
	rev := "HEAD"
	if read != ws {
		rev = "refs/heads/" + ch.Branch
	}
	sha, err = gitexec.Output(ctx, read, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil && read != ws {
		return "", "", false, &noWorkspaceError{ch.ID} // no folder, and no branch saved either
	}
	if err != nil {
		return "", "", false, fmt.Errorf("reading the commit to check: %w", err)
	}
	return read, sha, false, nil
}

// publishedTip is the tip of ch's branch in the project's repository hub.
// No repository, or no branch there, is nothing published; a branch git
// cannot read as a commit is an error.
func publishedTip(ctx context.Context, hub string, ch *workflow.Change) (repo, sha string, err error) {
	ref := "refs/heads/" + ch.Branch
	if ch.Branch == "" || !gitexec.IsRepo(hub) {
		return "", "", &NothingPublishedError{Change: ch.ID, Branch: ch.Branch}
	}
	if _, err := gitexec.OutputBytes(ctx, hub, nil, "show-ref", "--verify", "--quiet", ref); err != nil {
		return "", "", &NothingPublishedError{Change: ch.ID, Branch: ch.Branch}
	}
	sha, err = gitexec.ResolveCommit(ctx, hub, ref)
	if err != nil || !gitexec.IsObjectID(sha) {
		return "", "", fmt.Errorf("reading the published tip of %s: %w", ref, err)
	}
	return hub, sha, nil
}

// NothingPublishedError refuses a check of a change's published tip when
// the forge has no branch for it: nothing has been published to check.
type NothingPublishedError struct {
	Change ids.ID
	Branch string
}

func (e *NothingPublishedError) Error() string {
	if e.Branch == "" {
		return fmt.Sprintf("change #%d has no branch, so nothing is published to check", e.Change)
	}
	return fmt.Sprintf("change #%d has nothing published to check: the forge has no branch %s for it", e.Change, e.Branch)
}
func (e *NothingPublishedError) FaultKind() fault.Kind { return fault.NotFound }

// within reports whether commit is tip or in its history in repo. Any
// failure (the commit absent, tip unborn) is "no": the caller refuses.
func within(ctx context.Context, repo, commit, tip string) bool {
	_, err := gitexec.OutputBytes(ctx, repo, nil, "merge-base", "--is-ancestor", commit, tip)
	return err == nil
}

// withinScratch reports whether commit is in the history of one of the
// change's scratch refs in hub. A ref Git cannot list is "no".
func withinScratch(ctx context.Context, hub string, changeID ids.ID, commit string) bool {
	refs, err := scm.ScratchRefs(ctx, hub, scm.ChangeScratchPrefix(changeID))
	if err != nil {
		return false
	}
	for ref := range refs {
		if within(ctx, hub, commit, ref) {
			return true
		}
	}
	return false
}

// CommitNotOnForgeError refuses a check of a commit the forge does not have
// as the change's work: typically an agent's commit on a worker that was
// not sent (the CLI pushes it to the step's scratch ref first), which
// otherwise reaches the forge only when the step's work is collected. It
// promises no check then: it says only when the commit can be asked about.
type CommitNotOnForgeError struct{ Commit string }

func (e *CommitNotOnForgeError) Error() string {
	return fmt.Sprintf("commit %s is not on the forge yet; it becomes available once the step's work is collected. `forge check --published` checks the last published commit", e.Commit)
}
func (e *CommitNotOnForgeError) FaultKind() fault.Kind { return fault.NotFound }

// mainHeadSHA is the protected branch's head in the project's repository.
func (v *Changes) mainHeadSHA(ctx context.Context, projectID ids.ID) string {
	project, err := project.ByIDIn(ctx, v.db, projectID)
	if err != nil || project.DefaultBranch == "" {
		return ""
	}
	out, err := gitexec.Run(ctx, filepath.Join(v.dataDir, "workspaces", "project-"+project.Slug), "rev-parse", "refs/heads/"+project.DefaultBranch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// baseProvenance says whether a base SHA is on the protected branch.
func (v *Changes) baseProvenance(ctx context.Context, projectID ids.ID, baseSHA string) (onMain, isMain bool) {
	head := v.mainHeadSHA(ctx, projectID)
	if head == "" || baseSHA == "" {
		return false, false
	}
	if head == baseSHA {
		return true, true
	}
	slug := projectOrEmpty(ctx, v.db, projectID).Slug
	_, err := gitexec.Run(ctx, filepath.Join(v.dataDir, "workspaces", "project-"+slug), "merge-base", "--is-ancestor", baseSHA, head)
	return err == nil, false
}

// baselineFor runs the deterministic checks against the base tree (cached
// per project, base and check set). Tool checks needing the app image use a
// cached image only — never a build for the baseline — and count as
// unknown when it is not there.
func (v *Changes) baselineFor(ctx context.Context, projectID ids.ID, checks []conformance.DeclaredCheck, baseSHA, prefix string) *baselineSet {
	if baseSHA == "" || len(checks) == 0 {
		return nil
	}
	// The cache key is every input the answer depends on (§4.2): the
	// tree, the checks, and the evaluators behind the tool checks.
	key := fmt.Sprintf("%d|%s|%s|%s", projectID, baseSHA, checksDigest(checks), v.toolIdentities(ctx, checks))
	c := v.baseline
	c.mu.Lock()
	if b := c.entries[key]; b != nil && (len(b.unknown) == 0 || v.now().Sub(b.at) < unknownRetry) {
		c.mu.Unlock()
		return b
	}
	c.mu.Unlock()
	slug := projectOrEmpty(ctx, v.db, projectID).Slug
	src, cleanup, err := scm.StageTreeAt(ctx, filepath.Join(v.dataDir, "workspaces", "project-"+slug), baseSHA)
	b := &baselineSet{failing: map[string]bool{}, items: map[string][]string{}, unknown: map[string]bool{}, sha: baseSHA, at: v.now()}
	b.onMain, b.isMain = v.baseProvenance(ctx, projectID, baseSHA)
	if err != nil {
		b.note = "no baseline: " + err.Error()
		return b
	}
	defer cleanup()
	results, findings, _ := v.evaluator.runShChecks(ctx, checks, src, prefix+"-base")
	toolResults, toolFindings, _ := v.scanners.RunToolChecks(ctx, checks,
		ToolContext{Context: ctx, ProjectID: projectID, SHA: baseSHA, BuildDir: src, AllowBuild: false}, prefix+"-base")
	results = append(results, toolResults...)
	findings = append(findings, toolFindings...)
	if ctx.Err() != nil {
		// A baseline its caller stopped is not kept: its unknowns would
		// mark the next check's findings undecided, and so not blocking.
		b.note = "no baseline: the check was stopped"
		return b
	}
	for _, f := range findings {
		b.failing[f.Fingerprint] = true
		b.items[f.Fingerprint] = append(b.items[f.Fingerprint], f.Items...)
	}
	for _, r := range results {
		if r.Verdict != workflow.VerdictPass && r.Verdict != workflow.VerdictFail {
			b.unknown[fmt.Sprintf("%s@%d|%s", r.Block, r.Version, r.Check)] = true
		}
	}
	c.mu.Lock()
	c.entries[key] = b
	c.mu.Unlock()
	return b
}

// toolIdentities digests the scanner bindings the tool checks run
// through — image and configuration — so a changed binding is a new
// baseline, never a stale one.
func (v *Changes) toolIdentities(ctx context.Context, checks []conformance.DeclaredCheck) string {
	var sb strings.Builder
	for _, sc := range checks {
		if check.Evaluator(sc.Check) != "tool" {
			continue
		}
		name, m, version, _, err := v.scanners.binding(ctx, sc.Check.Tool)
		if err != nil {
			fmt.Fprintf(&sb, "%s:missing;", sc.Check.Tool)
			continue
		}
		fmt.Fprintf(&sb, "%s=%s@%d:%s;", sc.Check.Tool, name, version, m.Image)
	}
	if sb.Len() == 0 {
		return "-"
	}
	return shortHash(sb.String())
}

// forkPointSHA is where a change's branch diverged from the project's default
// branch — the merge-base, computed in the project repo (which holds both) —
// the honest "base" for provenance even after catch-up merges. "" when unknown.
func (v *Changes) forkPointSHA(ctx context.Context, projectID ids.ID, branch string) string {
	project, err := project.ByIDIn(ctx, v.db, projectID)
	if err != nil || project.DefaultBranch == "" || branch == "" {
		return ""
	}
	slug, def := project.Slug, project.DefaultBranch
	repo := filepath.Join(v.dataDir, "workspaces", "project-"+slug)
	if !gitexec.IsRepo(repo) {
		return ""
	}
	out, err := gitexec.Run(ctx, repo, "merge-base", def, branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

type noWorkspaceError struct{ id ids.ID }

func (e *noWorkspaceError) Error() string {
	return fmt.Sprintf("change %d has no repo workspace to check", e.id)
}
func (e *noWorkspaceError) Unwrap() error { return scm.ErrNoWorkspace }

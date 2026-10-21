package verification

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/jobs"
	"flokkit.io/forge/internal/forge/modelcall"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/remediation"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/strs"
)

// What-if on a pack version (design/subsystems/conformance.md, "Pack versions
// and what-if"): for
// each selected project, on the pack or not, the candidate Adoption would
// prepare for that version, its checks and the project's adopted ones judged
// on the same committed main, compared by block and check name. Evaluation
// is assessChecks; nothing here begins a review or writes a verdict. Shell
// and tool checks run on the tree, an image tool building within the fleet
// review's build budget; runtime probes ask the deployment that serves this
// exact commit; model rubrics are judged only when the starter included
// them, under a spend limit asked before each project's checklist. A check
// not judged says why.

// CandidatePreparer is Adoption's preparation of a candidate: it describes
// what a project would adopt and changes nothing. ProjectsBelow is who holds
// a pack below a version, among the projects the caller may read.
type CandidatePreparer interface {
	Prepare(ctx context.Context, projectID ids.ID, req adoption.Request) (*adoption.Candidate, error)
	PicksWith(ctx context.Context, projectID ids.ID, pack string, version int64, also ...adoption.PackPick) ([]adoption.PackPick, error)
	ProjectsBelow(ctx context.Context, pack string, version int64) ([]adoption.ProjectRef, error)
}

// StartWhatIf admits a what-if as queued, with the job that runs it; its
// record says how far it got. A request that names no projects (nil) runs
// on every eligible one, which admission reads after it checks the
// starter's authority.
func (f Fleet) StartWhatIf(ctx context.Context, in conformance.WhatIfRequest) (ids.ID, error) {
	if f.candidates == nil {
		return ids.Nil, errors.New("what-if needs Adoption's candidate preparation")
	}
	return database.Transact(ctx, f.db, func(tx *database.Tx) (ids.ID, error) {
		id, err := f.checks.AdmitWhatIfIn(ctx, tx, in, func() ([]ids.ID, error) {
			below, err := belowAny(in.Pack, in.Version, in.Also, func(pack string, version int64) ([]adoption.ProjectRef, error) {
				return adoption.ProjectsBelowIn(ctx, tx, pack, version)
			})
			if err != nil {
				return nil, err
			}
			active, err := activeOnly(ctx, tx, below)
			idList := make([]ids.ID, 0, len(active))
			for _, p := range active {
				idList = append(idList, p.ID)
			}
			return idList, err
		})
		if err != nil {
			return ids.Nil, err
		}
		return id, f.jobs.EnqueueIn(ctx, tx, jobs.Request{Operation: WhatIfJob, Key: id.String()})
	})
}

// EligibleForWhatIf is who a what-if of the version runs on when it names
// no projects, among those the caller may read: each active ordinary
// project with a repository that directly holds the pack below the version.
func (f Fleet) EligibleForWhatIf(ctx context.Context, pack string, version int64, also ...conformance.PackVersion) ([]string, error) {
	if f.candidates == nil {
		return nil, errors.New("what-if needs Adoption's candidate preparation")
	}
	below, err := belowAny(pack, version, also, func(pack string, version int64) ([]adoption.ProjectRef, error) {
		return f.candidates.ProjectsBelow(ctx, pack, version)
	})
	if err != nil {
		return nil, err
	}
	active, err := activeOnly(ctx, f.db, below)
	if err != nil {
		return nil, err
	}
	slugs := make([]string, 0, len(active))
	for _, p := range active {
		slugs = append(slugs, p.Slug)
	}
	return slugs, nil
}

// belowAny is who holds the named pack below its version, or an also pack
// below its: eligibility comes from the whole request, so a project
// current on the named pack but behind on its own is found. Order: first
// seen, without repeats.
func belowAny(pack string, version int64, also []conformance.PackVersion, below func(string, int64) ([]adoption.ProjectRef, error)) ([]adoption.ProjectRef, error) {
	out, err := below(pack, version)
	if err != nil {
		return nil, err
	}
	seen := map[ids.ID]bool{}
	for _, r := range out {
		seen[r.ID] = true
	}
	for _, a := range also {
		more, err := below(a.Pack, a.Version)
		if err != nil {
			return nil, err
		}
		for _, r := range more {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// AlsoPicks is a what-if's other pack versions as Adoption's picks.
func AlsoPicks(also []conformance.PackVersion) []adoption.PackPick {
	out := make([]adoption.PackPick, 0, len(also))
	for _, a := range also {
		out = append(out, adoption.PackPick{Pack: a.Pack, Version: a.Version})
	}
	return out
}

// activeOnly keeps the projects that take new work: one being imported,
// being deleted or kept deleted is left out, as the fleet review leaves it.
func activeOnly(ctx context.Context, q database.Queryer, refs []adoption.ProjectRef) ([]adoption.ProjectRef, error) {
	out := make([]adoption.ProjectRef, 0, len(refs))
	for _, r := range refs {
		l, err := project.LifecycleIn(ctx, q, r.ID)
		if err != nil {
			return nil, err
		}
		if l.State == project.Active {
			out = append(out, r)
		}
	}
	return out, nil
}

// RunWhatIfJob runs the what-if its key names, if this delivery claims it,
// as it was asked. Its outcome is on the what-if: a delivery that finds it
// claimed or ended does nothing, and one stopped by shutdown or the
// queue's time limit ends interrupted with the rows it recorded.
func (f Fleet) RunWhatIfJob(ctx context.Context, key string, _ map[string]any) (map[string]any, error) {
	id, err := ids.Parse(key)
	if err != nil {
		return nil, fmt.Errorf("what-if job key %q: %w", key, err)
	}
	run, in, err := f.checks.ClaimWhatIf(ctx, id)
	if errors.Is(err, conformance.ErrNotQueued) {
		return map[string]any{"whatIf": id, "ran": false}, nil
	}
	if err != nil {
		return nil, err
	}
	walkErr := f.walkWhatIf(ctx, run, in)
	if walkErr != nil && ctx.Err() != nil {
		walkErr = fmt.Errorf("%w: %w", conformance.ErrInterrupted, walkErr)
	}
	if walkErr != nil {
		f.reporting.Report("what-if", "conformance/what-if", walkErr.Error(), map[string]string{"whatIf": fmt.Sprint(id)}, "")
	}
	if err := run.Finish(context.WithoutCancel(ctx), walkErr); err != nil {
		f.reporting.Report("what-if", "conformance/what-if", "settling: "+err.Error(), map[string]string{"whatIf": fmt.Sprint(id)}, "")
		return nil, err
	}
	return map[string]any{"whatIf": id, "ran": true}, nil
}

func (f Fleet) walkWhatIf(ctx context.Context, run *conformance.WhatIfRun, in conformance.WhatIfRequest) error {
	projects, err := run.Projects(ctx)
	if err != nil {
		return err
	}
	builds := 0
	for _, p := range projects {
		rows, spend, err := f.whatIfProject(ctx, run, in, p, &builds)
		if err != nil {
			return fmt.Errorf("%s: %w", p.Slug, err)
		}
		if err := run.Record(ctx, rows, spend); err != nil {
			return err
		}
	}
	return nil
}

// whatIfProject answers one project. A project that cannot be judged gets
// one row saying why, never an empty answer. builds counts the image builds
// the what-if has spent of the fleet budget.
func (f Fleet) whatIfProject(ctx context.Context, run *conformance.WhatIfRun, in conformance.WhatIfRequest, p conformance.Project, builds *int) ([]conformance.WhatIfRow, conformance.WhatIfSpend, error) {
	var spend conformance.WhatIfSpend
	joins, err := joinsPack(ctx, f, p.ID, in.Pack)
	if err != nil {
		return nil, spend, err
	}
	whole := func(sha string, why conformance.NotAssessed, detail string) []conformance.WhatIfRow {
		return []conformance.WhatIfRow{{ProjectID: p.ID, ProjectSlug: p.Slug, SHA: sha, Result: conformance.WhatIfNotAssessed, Reason: why, Joins: joins,
			Detail: strings.TrimSuffix(why.Words()+": "+detail, ": ")}}
	}
	head, err := run.Source(ctx, p)
	if err != nil {
		return nil, spend, err
	}
	if head.SHA == "" {
		return whole("", conformance.NotAssessedNoMain, head.Unavailable), spend, nil
	}
	picks, err := f.candidates.PicksWith(ctx, p.ID, in.Pack, in.Version, AlsoPicks(in.Also)...)
	if err != nil {
		return nil, spend, err
	}
	cand, err := f.candidates.Prepare(ctx, p.ID, adoption.Request{Packs: picks, AdvanceWorkflows: in.AdvanceWorkflows})
	if err != nil {
		return whole(head.SHA, conformance.NotAssessedNotAdoptable, err.Error()), spend, nil
	}
	if len(cand.Problems) > 0 {
		return whole(head.SHA, conformance.NotAssessedNotAdoptable, strings.Join(cand.Problems, "; ")), spend, nil
	}
	var blocks []conformance.CheckVersion
	for _, b := range cand.Receipt.Guidance {
		if !b.Disabled {
			blocks = append(blocks, conformance.CheckVersion{Block: b.Name, Version: b.Version})
		}
	}
	proposed, err := run.ChecksAt(ctx, blocks)
	if err != nil {
		return nil, spend, err
	}
	exceptions, err := run.Exceptions(ctx, p.ID)
	if err != nil {
		return nil, spend, err
	}

	// Each distinct check is judged once: a block version both sides carry
	// is one evaluation answering both.
	type key struct {
		block, name string
		version     int
	}
	seen := map[key]bool{}
	var distinct []conformance.DeclaredCheck
	tools, runtime := 0, 0
	for _, sc := range append(append([]conformance.DeclaredCheck{}, p.Checks...), proposed...) {
		k := key{sc.Block, sc.Check.Name, sc.Version}
		if seen[k] {
			continue
		}
		seen[k] = true
		distinct = append(distinct, sc)
		switch {
		case check.Evaluator(sc.Check) == "tool":
			tools++
		case check.IsRuntime(sc.Check):
			runtime++
		}
	}
	outcomes := map[key]treeAssessment{}
	if len(distinct) > 0 {
		src, cleanup, err := scm.StageTreeAt(ctx, projectRepo(f, p), head.SHA)
		if err != nil {
			return whole(head.SHA, conformance.NotAssessedNoMain, "could not read main: "+strs.Truncate(err.Error(), 200)), spend, nil
		}
		allowBuild := tools > 0 && *builds < defaultBuildBudget
		if allowBuild {
			*builds++
		}
		assess := assessInput{Tool: ToolContext{Context: ctx, Exceptions: append([]conformance.Disposition{}, exceptions...), ProjectID: p.ID, SHA: head.SHA, BuildDir: src, AllowBuild: allowBuild},
			Label: fmt.Sprintf("apptown-what-if-%d-%d", run.ID(), p.ID)}
		if runtime > 0 {
			assess.Running, assess.NoApp = f.servingCommit(ctx, p.ID, head.SHA)
		}
		assess.Judge, assess.NoJudge, err = f.whatIfJudge(ctx, run, in, p.ID, &spend)
		if err != nil {
			cleanup()
			return nil, spend, err
		}
		for _, a := range f.assessChecks(ctx, distinct, assess) {
			outcomes[key{a.Check.Block, a.Check.Check.Name, a.Check.Version}] = a
		}
		cleanup()
	}
	// The outcome of one side's check. Both sides keep the exception
	// matcher's answer, as adoption would: a whole-check exception names its
	// block version, so it stops covering a block the version moves, and an
	// identifier exception (a scanner rule) covers the check at any version.
	outcomeOf := func(sc conformance.DeclaredCheck) treeAssessment {
		a, ok := outcomes[key{sc.Block, sc.Check.Name, sc.Version}]
		if !ok {
			return treeAssessment{Check: sc, State: conformance.CannotAssess, Reason: conformance.NotAssessedError, Detail: "no answer was recorded for this check"}
		}
		return a
	}

	byName := func(checks []conformance.DeclaredCheck) (map[[2]string]treeAssessment, [][2]string) {
		out, order := map[[2]string]treeAssessment{}, [][2]string{}
		for _, sc := range checks {
			n := [2]string{sc.Block, sc.Check.Name}
			if _, dup := out[n]; dup {
				continue
			}
			out[n], order = outcomeOf(sc), append(order, n)
		}
		return out, order
	}
	now, nowOrder := byName(p.Checks)
	next, nextOrder := byName(proposed)
	names := nextOrder
	for _, n := range nowOrder {
		if _, both := next[n]; !both {
			names = append(names, n)
		}
	}
	var rows []conformance.WhatIfRow
	for _, n := range names {
		cur, had := now[n]
		pro, has := next[n]
		row := conformance.WhatIfRow{ProjectID: p.ID, ProjectSlug: p.Slug, SHA: head.SHA, AdoptionRevision: cand.ExpectedRevision,
			CandidateDigest: cand.Digest, Block: n[0], Check: n[1], Joins: joins}
		var curO, proO *conformance.Outcome
		if had {
			o := cur.State
			curO, row.CurrentVersion, row.Current = &o, cur.Check.Version, o.String()
		}
		if has {
			o := pro.State
			proO, row.ProposedVersion, row.Proposed, row.Detail = &o, pro.Check.Version, o.String(), pro.Detail
		}
		row.Result = conformance.WhatIfResult(curO, proO)
		if row.Result == conformance.WhatIfNotAssessed {
			// Why: the version's side when it was not judged, else today's.
			why := pro
			if pro.Reason == "" {
				why = cur
			}
			row.Reason, row.Detail = why.Reason, strings.TrimSuffix(why.Reason.Words()+": "+why.Detail, ": ")
		}
		if had && has && cur.State == conformance.Excepted && pro.State == conformance.DoesNotConform {
			row.Detail = strings.TrimSpace("excepted today; the exception does not cover this version. " + row.Detail)
		}
		if row.Result == conformance.WhatIfWouldFail {
			row.Brief = remediation.Brief(pro.Check.Block, pro.Check.Version, pro.Check, pro.Detail)
			if row.History, err = remediation.AttemptHistoryIn(ctx, f.db, n[0], n[1]); err != nil {
				return nil, spend, err
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = whole(head.SHA, conformance.NotAssessedNoEvaluator, "neither today's checks nor the version's apply to this project")
	}
	// Every rule whose version the candidate changes is a row of its own, so
	// a version that takes a rule back, drops it or moves a workflow is
	// never read as "no new failures". A version fact never replaces the
	// answer above that nothing could be judged.
	for _, m := range cand.Moves {
		rows = append(rows, conformance.WhatIfRow{ProjectID: p.ID, ProjectSlug: p.Slug, SHA: head.SHA, AdoptionRevision: cand.ExpectedRevision,
			CandidateDigest: cand.Digest, Block: m.Rule, CurrentVersion: m.From, ProposedVersion: m.To, Result: conformance.RuleMoveResult(m.From, m.To), Joins: joins, Detail: m.String()})
	}
	return rows, spend, nil
}

// joinsPack says the project is not on the pack: what-if asks what
// joining it would say.
func joinsPack(ctx context.Context, f Fleet, projectID ids.ID, pack string) (bool, error) {
	current, err := adoption.ProjectPacksIn(ctx, f.db, projectID)
	if err != nil {
		return false, err
	}
	for _, m := range current {
		if m.Pack == pack {
			return false, nil
		}
	}
	return true, nil
}

// servingCommit is the project's standing deployment when it serves
// exactly sha, or why it is not there to probe. The app a version would
// run is the same app: a pack carries no build or runtime declaration (the
// app's contract is in its tree), so one probe answers for both sides.
func (f Fleet) servingCommit(ctx context.Context, projectID ids.ID, sha string) (*runningApp, string) {
	if f.runtime == nil {
		return nil, "AppTown cannot observe deployments here"
	}
	t, commit, still, why := f.runtime.mainAt(ctx, projectID)
	switch {
	case why != "":
		return nil, why
	case commit != sha:
		return nil, fmt.Sprintf("main's deployment runs %s, not %s", strs.ShortSHA(commit), strs.ShortSHA(sha))
	}
	return &runningApp{Target: t, Still: still}, ""
}

// whatIfJudge is how a project's model rubrics are judged in a what-if:
// not at all unless the starter included them, and then only while the
// what-if's observed spend is under its limit and every earlier call's
// spend is known. The rule is asked before the project's one checklist;
// that checklist may be two paid calls (the evaluator asks again when an
// answer is not JSON), which is the accepted overshoot. What it cost is
// added to spend; each paid call's own line in the usage ledger is the
// gateway's, charged to the project assessed with the what-if as actor.
func (f Fleet) whatIfJudge(ctx context.Context, run *conformance.WhatIfRun, in conformance.WhatIfRequest, projectID ids.ID, spend *conformance.WhatIfSpend) (func([]check.Item, string) (map[int]check.Verdict, *llmMetadata, error), conformance.NotAssessed, error) {
	if !in.Rubrics {
		return nil, conformance.NotAssessedRubricsNotIncluded, nil
	}
	spent, limit, err := run.RubricSpend(ctx)
	if err != nil {
		return nil, "", err
	}
	switch {
	case spent.Incomplete > 0:
		return nil, conformance.NotAssessedSpendUnknown, nil
	case spent.CostMicros >= limit:
		return nil, conformance.NotAssessedSpendLimit, nil
	}
	return func(items []check.Item, diff string) (map[int]check.Verdict, *llmMetadata, error) {
		by, meta, err := f.judgeChecklist(ctx, modelcall.Call{Purpose: "what-if", ProjectID: projectID, Actor: fmt.Sprintf("what-if/%d", run.ID())}, items, diff)
		if meta != nil && meta.Usage != nil {
			u := meta.Usage
			spend.CostMicros += int64(u.Cost * 1e6)
			if u.Incomplete != "" {
				spend.Incomplete++
			}
		}
		return by, meta, err
	}, "", nil
}

// projectRepo is where a project's repository lives, as the review reads it.
func projectRepo(f Fleet, p conformance.Project) string {
	return filepath.Join(f.dataDir, "workspaces", "project-"+p.Slug)
}

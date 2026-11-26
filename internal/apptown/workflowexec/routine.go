package workflowexec

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/imagelines"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/verification"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/packagefacts"
)

type updateBounds interface {
	UpdateChoicesForRun(context.Context, ids.ID) (workflow.JanitorChoices, error)
}
type updateJudge interface {
	JudgeUpdate(context.Context, workflow.Source, check.Item, string) (workflow.CheckReceipt, error)
}

// majorDecider decides direct dependency and image moves to another
// release line (verification.Majors).
type majorDecider interface {
	Check(context.Context, ids.ID, []packagefacts.Dependency, []packagefacts.Dependency, bool) ([]verification.MajorMove, error)
	Images(verification.ImageUpdates) verification.ImageUpdates
}
type routineConfig struct {
	Rubric string `json:"rubric"`
}

func (a *commands) runUpdateRoutine(task *workflow.Execution) {
	if _, err := task.Claim(); err != nil {
		return
	}
	var classified []fix.File
	var receipt workflow.CheckReceipt
	var readings []imagelines.Reading
	var majors []verification.MajorMove
	finish := func(status workflow.StepState, why string) {
		verdict := workflow.VerdictError
		switch status {
		case workflow.StatusPass:
			verdict = workflow.VerdictPass
		case workflow.StatusFail:
			verdict = workflow.VerdictFail
		case workflow.StatusNotApplicable:
			verdict = workflow.VerdictNotApplicable
		}
		if status == workflow.StatusError && receipt.Verdict == workflow.VerdictCannotAssess {
			verdict = workflow.VerdictCannotAssess
		}
		receipt.Check, receipt.Evaluator, receipt.Verdict, receipt.Detail = task.Step().Name, "routine", verdict, why
		facts := workflow.CheckEvidence{Checks: []workflow.CheckReceipt{receipt}}
		extra := map[string]any{"reason": why, "files": classified, "imageReadings": readings}
		if len(majors) > 0 {
			extra["majorMoves"] = majors
		}
		task.Settle(facts.Outcome(status, extra))
	}
	fail := func(err error) { receipt.AppTownFault = false; finish(workflow.StatusError, err.Error()) }
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: task.Run().ID, ProjectID: task.Run().ProjectID})
	if err != nil {
		fail(err)
		return
	}
	sel, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, task.Run(), source)
	if err != nil {
		fail(err)
		return
	}
	p, err := a.prepareScope(task.Context(), task.Run(), sel.Repo, sel.Commit)
	if err != nil {
		fail(err)
		return
	}
	classified = p.Files
	if p.Early != nil {
		finish(p.Early.Status, p.Early.Reason)
		return
	}
	moves := verification.UpdateInputMoves(p.Inputs[0], p.Inputs[1])
	for _, old := range p.Direct[0] {
		for _, next := range p.Direct[1] {
			if old.Package == next.Package && old.Manifest == next.Manifest && old.Version != next.Version {
				moves = append(moves, workflow.PackageMove{Ecosystem: string(old.Type), Package: old.Name, From: old.Version, To: next.Version})
			}
		}
	}
	if err := task.RecordPackageMoves(moves); err != nil {
		fail(err)
		return
	}
	queue, err := task.IsQueue()
	if err != nil {
		fail(err)
		return
	}
	if queue {
		main, err := gitexec.HubReadRef(task.Context(), sel.Repo, "refs/heads/"+source.DefaultBranch)
		if err != nil {
			fail(err)
			return
		}
		tree, err := gitexec.HubRun(task.Context(), sel.Repo, "rev-parse", sel.Commit+"^{tree}")
		if err != nil {
			fail(err)
			return
		}
		same, why, err := task.KeepProposal(main, strings.TrimSpace(tree), classified)
		if err != nil {
			fail(err)
			return
		}
		if same {
			task.Settle(workflow.EncodeOutcome(workflow.StatusSkipped, map[string]any{"reason": why, "sameProposal": true}, nil))
			if err := task.StopDuplicate(why); err != nil {
				return
			}
			return
		}
	}
	var cfg routineConfig
	if err := workflow.DecodeStepConfig(task.Step().Config, &cfg); err != nil {
		fail(err)
		return
	}
	j, err := a.judgeScope(task.Context(), task.Run(), task.Step().Name, cfg.Rubric, p)
	receipt, readings, majors = j.Receipt, j.Readings, j.Majors
	if err != nil {
		fail(err)
		return
	}
	// Only a model judgement reaches the candidate re-read, as before.
	if j.Facts != "" {
		last, err := verification.SelectCommit(task.Context(), a.db, a.cfg.DataDir, task.Run(), source)
		if err != nil || !reflect.DeepEqual(sel, last) {
			fail(fmt.Errorf("the candidate moved while it was classified"))
			return
		}
	}
	finish(j.Status, j.Reason)
}

type preparedScope struct {
	Choices                   workflow.JanitorChoices
	Source                    workflow.Source
	Repo, Commit, Base, Brief string
	Targets                   []check.Finding
	Inputs                    [2]map[string]any
	Direct                    [2][]packagefacts.Dependency
	Files                     []fix.File
	Early                     *scopeJudgement
}
type scopeJudgement struct {
	Status        workflow.StepState
	Reason, Facts string
	// Unreadable marks a fail that judged nothing: AppTown could not read
	// the dependency inputs. The routine step keeps its fail; feedback
	// reports it as an error, never as a scope verdict.
	Unreadable bool
	Receipt    workflow.CheckReceipt
	Readings   []imagelines.Reading
	Majors     []verification.MajorMove
}

func scopeBrief(choices workflow.JanitorChoices, targets []check.Finding) string {
	facts := "\n\nApproved level: " + choices.Dependencies
	if choices.Dependencies == "aggressive" {
		facts += " (within each release line; a direct dependency or image moves to another line only where listed below)"
	} else {
		facts += " (within each release line)"
	}
	if choices.BaseImages {
		facts += "\nBase image updates: on"
	} else {
		facts += "\nBase image updates: off"
	}
	if choices.ToolchainPatches {
		facts += "\nToolchain patches: on"
	} else {
		facts += "\nToolchain patches: off"
	}
	if len(targets) > 0 {
		facts += "\nFindings to fix:"
		for _, t := range targets {
			if t.Package != nil {
				facts += fmt.Sprintf(" %s %s (%s)", t.Package.Name, t.Package.Version, strings.Join(t.Identifiers, ", "))
			}
		}
	}
	return facts
}

// prepareScope reads settings and committed inputs without registry or model calls.
func (a *commands) prepareScope(ctx context.Context, run *workflow.Run, repo, commit string) (p preparedScope, err error) {
	p.Repo, p.Commit = repo, commit
	if a.bounds == nil {
		return p, fmt.Errorf("update bounds are unavailable")
	}
	choices, err := a.bounds.UpdateChoicesForRun(ctx, run.ID)
	if err != nil {
		return p, err
	}
	targets, err := runTargets(run)
	if err != nil {
		return p, err
	}

	p.Choices, p.Targets = choices, targets
	p.Brief = scopeBrief(choices, targets)
	bound := choices.Dependencies
	if bound == "" || bound == "off" {
		p.Early = &scopeJudgement{Status: workflow.StatusNotApplicable, Reason: "no eligible update rule"}
		return p, nil
	}
	source, err := workflow.ReadSourceIn(ctx, a.db, workflow.RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return p, err
	}
	base, err := scm.ForkBase(ctx, repo, repo, source.DefaultBranch, commit, source.ChangeBaseSHA)
	if err != nil {
		return p, err
	}
	changes, err := scm.ReadObjectChanges(ctx, repo, base, commit)
	if err != nil {
		return p, err
	}

	// The routine judges the change. An empty change has no diff to judge;
	// whether it may close is decided by AppTown's own steps (tests, start-up,
	// scans), never by the agent's account of why nothing moved.
	if len(changes) == 0 {
		p.Early = &scopeJudgement{Status: workflow.StatusNotApplicable, Reason: "nothing changed; there is no diff to judge"}
		return p, nil
	}
	ds, err := a.declarations(ctx, run)
	if err != nil {
		return p, err
	}
	var inputs [2]map[string]any
	// A direct dependency stays on its release line at Routine; at
	// aggressive it leaves it only when AppTown's rules say it is due. The
	// lockfiles say which ones moved.
	if a.majors == nil {
		return p, fmt.Errorf("release line facts are unavailable")
	}
	var direct [2][]packagefacts.Dependency
	for i, commit := range []string{base, commit} {
		tree, remove, err := verification.StageCommittedTree(ctx, a.cfg.DataDir, repo, commit)
		if err != nil {
			return p, err
		}
		inputs[i], err = verification.UpdateInputs(tree)
		if err == nil {
			if direct[i], err = packagefacts.Direct(tree); err != nil {
				remove()
				p.Early = &scopeJudgement{Status: workflow.StatusFail, Unreadable: true, Reason: "AppTown could not read the direct dependencies to check their release lines: " + err.Error()}
				return p, nil
			}
		}
		remove()
		if err != nil {
			return p, err
		}
	}
	paths := make([]string, 0, len(changes))
	for _, f := range changes {
		paths = append(paths, f.Path)
	}
	p.Files = fix.Classify(paths, ds, inputs[:]...)
	p.Source, p.Base, p.Inputs, p.Direct = source, base, inputs, direct
	return p, nil
}

// judgeScope compares images and release lines, then judges the complete diff.
func (a *commands) judgeScope(ctx context.Context, run *workflow.Run, name, rubric string, p preparedScope) (j scopeJudgement, err error) {
	choices, targets, inputs, direct := p.Choices, p.Targets, p.Inputs, p.Direct
	bound := choices.Dependencies
	aggressive := bound == "aggressive"
	var receipt workflow.CheckReceipt
	var readings []imagelines.Reading
	var majors []verification.MajorMove
	defer func() { j.Receipt, j.Readings, j.Majors = receipt, readings, majors }()
	var imageLines []verification.MajorMove
	images := a.majors.Images(verification.ImageUpdates{Resolver: a.images, Scope: verification.UpdateScope(bound), Security: len(targets) > 0, ToolchainPatches: choices.ToolchainPatches, Readings: &readings, Lines: &imageLines})
	err = verification.CompareUpdateInputs(ctx, inputs[0], inputs[1], choices.BaseImages, images)
	majors = append(majors, imageLines...)
	if err != nil {
		if fault.KindOf(err) == fault.Unavailable {
			receipt.Verdict = workflow.VerdictCannotAssess
			j.Status, j.Reason = workflow.StatusError, err.Error()
		} else {
			j.Status, j.Reason = workflow.StatusFail, err.Error()
		}
		return j, nil
	}
	decided, err := a.majors.Check(ctx, run.ProjectID, direct[0], direct[1], aggressive)
	if err != nil {
		return j, err
	}
	majors = append(majors, decided...)
	var refused []string
	for _, m := range decided {
		if !m.Allowed {
			refused = append(refused, m.Text())
		}
	}
	if len(refused) > 0 {
		j.Status, j.Reason = workflow.StatusFail, "A direct dependency moves to another release line that is not allowed:\n"+strings.Join(refused, "\n")
		return j, nil
	}
	var unknownLines []string
	for _, m := range majors {
		if m.LineUnknown != "" {
			unknownLines = append(unknownLines, m.LineUnknown+"; held to "+m.FromLine)
		}
	}
	if strings.TrimSpace(rubric) == "" {
		return j, fmt.Errorf("routine needs a dependency update rubric")
	}
	facts := p.Brief
	var lineMoves []verification.MajorMove
	for _, m := range majors {
		if m.FromLine != m.ToLine {
			lineMoves = append(lineMoves, m)
		}
	}
	if aggressive {
		facts += "\nRelease line moves AppTown allowed:"
		if len(lineMoves) == 0 {
			facts += " none"
		}
		for _, m := range lineMoves {
			facts += "\n  " + m.Text()
		}
	}
	if len(unknownLines) > 0 {
		facts += "\nUnknown release lines: " + strings.Join(unknownLines, "; ")
	}
	facts += "\nChanged files:"
	for _, class := range []string{"dependency", "toolchain", "other"} {
		var names []string
		for _, f := range p.Files {
			if f.Class == class {
				names = append(names, f.Path)
			}
		}
		if len(names) > 0 {
			facts += "\n  " + class + ": " + strings.Join(names, ", ")
		}
	}
	diff, err := gitexec.Run(ctx, p.Repo, "diff", p.Base+".."+p.Commit)
	if err != nil {
		return j, err
	}
	declaration := check.Declared{Identity: check.Identity{Namespace: run.WorkflowName, Version: run.WorkflowVersion}, Declaration: check.Declaration{Name: name, Expects: "The dependency update stays within its approved level and scope.", Evaluator: "llm", Scope: check.ScopeDiff, Rubric: rubric + facts}}
	item := check.DiffItems([]check.Declared{declaration}, diff)[0]
	item.Repository, item.Commit = p.Repo, p.Commit
	if a.judge == nil {
		return j, fmt.Errorf("dependency update judge is unavailable")
	}
	receipt, err = a.judge.JudgeUpdate(ctx, p.Source, item, diff)
	if err != nil {
		return j, err
	}
	status := workflow.StatusError
	switch receipt.Verdict {
	case workflow.VerdictPass:
		status = workflow.StatusPass
	case workflow.VerdictFail:
		status = workflow.StatusFail
	}
	why := receipt.Detail
	if len(lineMoves) > 0 {
		why += "\nRelease line moves:"
		for _, m := range lineMoves {
			why += "\n" + m.Text()
		}
	}
	if len(unknownLines) > 0 {
		why += "\nUnknown release lines: " + strings.Join(unknownLines, "; ")
	}
	j.Status, j.Reason, j.Facts = status, why, facts
	return j, nil
}

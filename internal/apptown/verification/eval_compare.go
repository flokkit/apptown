package verification

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/strs"
)

// An eval's comparison (design/subsystems/conformance.md, "Pack versions and
// what-if"):
// one arm's landed main judged against its Change's base, over the checks
// both arms' ways of working declare, so a check only one version declares
// still counts on both arms. It is the same assessment a what-if makes,
// given the arm's own deployment for runtime probes and the eval's one
// judge for model rubrics. It records nothing: the eval keeps the answer.
//
// Findings are compared, not pass/fail: a check failing on the base and on
// the arm can still hide a new finding. The provenance rule the landing
// uses (check.Classify) says what is new and what was already there; the
// same rule with the trees swapped says what the arm fixed. A check that
// did not answer on both trees (an error, a timeout, an unreadable report,
// not assessed) has no new or fixed findings: they are not comparable, with
// that side's reason.

// ArmComparison asks for one arm's landed work to be compared with its
// Change's base.
type ArmComparison struct {
	EvalID ids.ID
	// ProjectID, Slug and Branch are the arm's copy and its main.
	ProjectID ids.ID
	Slug      string
	Branch    string
	// BaseSHA is the Change's base: where the copy started.
	BaseSHA string
	// Blocks are the block versions both arms adopt: the union is judged.
	Blocks []conformance.CheckVersion
	// Judge is the model that judges both arms' rubrics; "" is none.
	Judge string
	// Base is the base's answers when another arm of the Change already
	// read them; nil reads them.
	Base []CheckAnswer
}

// CheckAnswer is one check's answer on one tree, kept so it can be read
// again (a Change's base, read once for both arms).
type CheckAnswer struct {
	Block    string                  `json:"block"`
	Version  int                     `json:"version"`
	Check    string                  `json:"check"`
	Kind     string                  `json:"kind"`
	State    string                  `json:"state"`
	Reason   conformance.NotAssessed `json:"reason,omitempty"`
	Detail   string                  `json:"detail,omitempty"`
	Findings []conformance.Finding   `json:"findings,omitempty"`
}

// ArmCompared is an arm's comparison: the commit judged, both trees'
// answers and one row per check.
type ArmCompared struct {
	Head string        `json:"head"`
	Base []CheckAnswer `json:"base"`
	Arm  []CheckAnswer `json:"arm"`
	Rows []CheckRow    `json:"rows"`
}

// CheckRow is one check's comparison: what each tree said, the findings
// the arm introduced, fixed or left as they were, or why its findings
// cannot be compared.
type CheckRow struct {
	Block         string   `json:"block"`
	Version       int      `json:"version"`
	Check         string   `json:"check"`
	Kind          string   `json:"kind"`
	Base          string   `json:"base"`
	Arm           string   `json:"arm"`
	New           []string `json:"new,omitempty"`
	Fixed         []string `json:"fixed,omitempty"`
	AlreadyThere  []string `json:"alreadyThere,omitempty"`
	NotComparable string   `json:"notComparable,omitempty"`
}

// CompareArm judges the union of both arms' checks on the arm's landed
// main and, unless given, on the Change's base.
func (f Fleet) CompareArm(ctx context.Context, in ArmComparison) (*ArmCompared, error) {
	// One tool versions snapshot for base and arm alike: a changed
	// evaluator is never read as a changed tree.
	sh := f.evaluator.snapshot().Images.Sh
	if in.ProjectID.IsZero() || in.Slug == "" || in.Branch == "" || in.BaseSHA == "" {
		return nil, errors.New("a comparison names the arm's copy, its main and the Change's base")
	}
	repo := filepath.Join(f.dataDir, "workspaces", "project-"+in.Slug)
	cmp, err := scm.ReadCommittedDiff(ctx, repo, in.BaseSHA, in.Branch)
	if err != nil {
		return nil, err
	}
	diff := ""
	if !cmp.Empty {
		if diff, err = cmp.Patch(ctx, repo); err != nil {
			return nil, err
		}
	}
	checks, err := f.checks.ChecksAt(ctx, in.Blocks)
	if err != nil {
		return nil, err
	}
	judge := f.evalJudgeFor(ctx, in)
	out := &ArmCompared{Head: cmp.Head, Base: in.Base}
	if out.Base == nil {
		if out.Base, err = f.answersAt(ctx, repo, cmp.Base, checks, assessInput{Sh: sh, NoApp: "the Change's base has no deployment to probe", Judge: judge,
			Label: fmt.Sprintf("apptown-eval-%d-%d-base", in.EvalID, in.ProjectID)}, in.ProjectID); err != nil {
			return nil, err
		}
	}
	arm := assessInput{Sh: sh, Diff: diff, Judge: judge, Label: fmt.Sprintf("apptown-eval-%d-%d", in.EvalID, in.ProjectID)}
	if hasRuntime(checks) {
		arm.Running, arm.NoApp = f.servingCommit(ctx, in.ProjectID, cmp.Head)
	}
	if out.Arm, err = f.answersAt(ctx, repo, cmp.Head, checks, arm, in.ProjectID); err != nil {
		return nil, err
	}
	declared := map[string]check.Declared{}
	for _, sc := range checks {
		declared[fmt.Sprintf("%s@%d|%s", sc.Block, sc.Version, sc.Check.Name)] = sc.Declared()
	}
	out.Rows = compareAnswers(out.Base, out.Arm, declared)
	return out, nil
}

// evalJudgeFor is how an arm's rubrics are judged: through the judge's own
// door (modelcall.Call.Judge, admitted by evals.Gate.AdmitJudge), with the
// eval's one judge model, the spend charged to the copy.
func (f Fleet) evalJudgeFor(ctx context.Context, in ArmComparison) func([]check.Item, string) (map[int]check.Verdict, *llmMetadata, error) {
	return func(items []check.Item, diff string) (map[int]check.Verdict, *llmMetadata, error) {
		if in.Judge == "" {
			return nil, nil, errors.New("no judge model: the baseline names no fast model")
		}
		return f.evaluator.Checklist(ctx, modelcall.Call{Purpose: "eval-score", ProjectID: in.ProjectID, Judge: true, Model: in.Judge}, items, diff)
	}
}

// answersAt stages one commit and answers every check on it.
func (f Fleet) answersAt(ctx context.Context, repo, sha string, checks []conformance.DeclaredCheck, in assessInput, projectID ids.ID) ([]CheckAnswer, error) {
	src, cleanup, err := scm.StageTreeAt(ctx, repo, sha)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", strs.ShortSHA(sha), err)
	}
	defer cleanup()
	in.Tool = ToolContext{Context: ctx, Exceptions: []conformance.Disposition{}, ProjectID: projectID, SHA: sha, BuildDir: src, AllowBuild: true}
	out := []CheckAnswer{}
	for _, a := range f.assessChecks(ctx, checks, in) {
		out = append(out, CheckAnswer{Block: a.Check.Block, Version: a.Check.Version, Check: a.Check.Check.Name, Kind: checkKind(a.Check.Check),
			State: a.State.String(), Reason: a.Reason, Detail: strs.Truncate(a.Detail, 500), Findings: conformance.WrapFindings(a.Findings)})
	}
	return out, nil
}

func hasRuntime(checks []conformance.DeclaredCheck) bool {
	for _, sc := range checks {
		if check.IsRuntime(sc.Check) {
			return true
		}
	}
	return false
}

// checkKind names how a check is judged, as the scorecard groups it.
func checkKind(c conformance.CheckDeclaration) string {
	switch {
	case check.IsPostureSh(c):
		return "shell"
	case check.Evaluator(c) == "tool":
		return "tool"
	case check.IsRuntime(c):
		return "runtime"
	case check.Evaluator(c) == "llm" && check.Scope(c) == conformance.ScopeTree:
		return "rubric"
	case check.Evaluator(c) == "llm":
		return "diff-rubric"
	}
	return "other"
}

// answered says a check answered on its tree: it passed, failed or was
// excused. Anything else has no findings to compare.
func (a CheckAnswer) answered() bool {
	switch a.State {
	case conformance.Conforms.String(), conformance.DoesNotConform.String(), conformance.Excepted.String():
		return true
	}
	return false
}

// evaluation is the answer as the provenance rule reads it. A tool's
// findings name the problems themselves (a scanner's fingerprints), so
// they are judged as a scanner's; every other check's finding stands for
// the check, with the occurrences it listed.
func (a CheckAnswer) evaluation(declared map[string]check.Declared) check.Evaluation {
	ev := check.Evaluation{Mode: check.Gate, Outcome: check.DoesNotConform, Findings: conformance.RawFindings(a.Findings)}
	if a.State == conformance.Conforms.String() {
		ev.Outcome = check.Conforms
	}
	if a.Kind != "tool" {
		ev.Declared = declared[answerKey(a)]
	}
	return ev
}

func answerKey(a CheckAnswer) string { return fmt.Sprintf("%s@%d|%s", a.Block, a.Version, a.Check) }

// whyNot is why an answer has no findings to compare, in its own words.
func (a CheckAnswer) whyNot() string {
	return strings.TrimSuffix(a.Reason.Words()+": "+a.Detail, ": ")
}

// compareAnswers is one row per check the arm was asked; declared is each
// check's declaration, which says whether it lists occurrences.
func compareAnswers(base, arm []CheckAnswer, declared map[string]check.Declared) []CheckRow {
	baseBy := map[string]CheckAnswer{}
	for _, b := range base {
		baseBy[answerKey(b)] = b
	}
	rows := make([]CheckRow, 0, len(arm))
	for _, a := range arm {
		b, ok := baseBy[answerKey(a)]
		if !ok {
			b = CheckAnswer{Block: a.Block, Version: a.Version, Check: a.Check, Kind: a.Kind, State: conformance.CannotAssess.String(), Reason: conformance.NotAssessedError, Detail: "the base was not asked"}
		}
		rows = append(rows, compareAnswer(b, a, declared))
	}
	return rows
}

// compareAnswer compares one check's answers on the base and the arm.
func compareAnswer(base, arm CheckAnswer, declared map[string]check.Declared) CheckRow {
	row := CheckRow{Block: arm.Block, Version: arm.Version, Check: arm.Check, Kind: arm.Kind, Base: base.State, Arm: arm.State}
	switch {
	case !arm.answered():
		row.NotComparable = "on this arm: " + arm.whyNot()
		return row
	case arm.Kind == "diff-rubric":
		// A diff rubric judges what the arm's change did; the base has no
		// change, so every finding is the arm's own.
		row.New = describe(conformance.RawFindings(arm.Findings))
		return row
	case !base.answered():
		row.NotComparable = "on the base: " + base.whyNot()
		return row
	}
	forward := check.Classify([]check.Evaluation{arm.evaluation(declared)}, []check.Evaluation{base.evaluation(declared)}, true)[0]
	back := check.Classify([]check.Evaluation{base.evaluation(declared)}, []check.Evaluation{arm.evaluation(declared)}, true)[0]
	if forward.Provenance == check.Undecided || back.Provenance == check.Undecided {
		row.NotComparable = "failing on both, and the check lists no occurrences to compare"
		return row
	}
	row.New, row.Fixed = describe(forward.Own), describe(back.Own)
	row.AlreadyThere = without(describe(conformance.RawFindings(arm.Findings)), row.New)
	return row
}

// without is all less what, one for one.
func without(all, what []string) []string {
	left := map[string]int{}
	for _, w := range what {
		left[w]++
	}
	var out []string
	for _, a := range all {
		if left[a] > 0 {
			left[a]--
			continue
		}
		out = append(out, a)
	}
	return out
}

// describe names findings for a reader: the occurrences a check listed, or
// the finding's title.
func describe(fs []check.Finding) []string {
	var out []string
	for _, f := range fs {
		if len(f.Items) > 0 {
			out = append(out, f.Items...)
			continue
		}
		out = append(out, strs.FirstNonBlank(f.Title, f.Fingerprint))
	}
	return out
}

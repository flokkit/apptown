package verification

import (
	"slices"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/check"
)

// An eval compares findings, not pass/fail, and only between trees where
// the check answered on both:
//   - a check failing on the base with old.go and on the arm with old.go
//     and new.go shows one new finding, new.go, and old.go already there;
//   - an arm that fixes old.go shows it fixed;
//   - a boolean check failing on both cannot say whether it got worse: not
//     comparable, never "same";
//   - a check that found old.go on the base and errored or timed out on
//     the arm is not comparable, with the arm's reason, never "fixed";
//   - a check that errored on the base and found new.go on the arm is not
//     comparable, never "new";
//   - a diff rubric judges the arm's change itself: its findings are new.
//
// Kills (EW-01, EW2-01): check-level equality hiding a new occurrence; an
// errored arm read as fixing everything; an errored base read as the arm
// introducing everything; a boolean failure on both counted as unchanged.
func TestAnEvalComparesFindingsOnlyWhereTheCheckAnsweredOnBothTrees(t *testing.T) {
	t.Parallel()
	lines := conformance.DeclaredCheck{Block: "hygiene", Version: 1, Check: conformance.CheckDeclaration{Name: "no-fixme", Sh: "grep -rl FIXME .", Occurrences: "lines"}}
	boolean := conformance.DeclaredCheck{Block: "hygiene", Version: 1, Check: conformance.CheckDeclaration{Name: "has-readme", Sh: "test -f README.md"}}
	diff := conformance.DeclaredCheck{Block: "style", Version: 1, Check: conformance.CheckDeclaration{Name: "small", Rubric: "r", Evaluator: "llm"}}
	declared := map[string]check.Declared{}
	for _, sc := range []conformance.DeclaredCheck{lines, boolean, diff} {
		declared[answerKey(CheckAnswer{Block: sc.Block, Version: sc.Version, Check: sc.Check.Name})] = sc.Declared()
	}
	failing := func(sc conformance.DeclaredCheck, items ...string) CheckAnswer {
		return CheckAnswer{Block: sc.Block, Version: sc.Version, Check: sc.Check.Name, Kind: checkKind(sc.Check), State: conformance.DoesNotConform.String(),
			Findings: conformance.WrapFindings([]check.Finding{{Fingerprint: conformance.Fingerprint(sc, ""), Title: sc.Check.Name, Items: items}})}
	}
	passing := func(sc conformance.DeclaredCheck) CheckAnswer {
		return CheckAnswer{Block: sc.Block, Version: sc.Version, Check: sc.Check.Name, Kind: checkKind(sc.Check), State: conformance.Conforms.String()}
	}
	errored := func(sc conformance.DeclaredCheck, detail string) CheckAnswer {
		return CheckAnswer{Block: sc.Block, Version: sc.Version, Check: sc.Check.Name, Kind: checkKind(sc.Check), State: conformance.EvaluationError.String(), Reason: conformance.NotAssessedError, Detail: detail}
	}
	row := func(base, arm CheckAnswer) CheckRow {
		return compareAnswers([]CheckAnswer{base}, []CheckAnswer{arm}, declared)[0]
	}

	if r := row(failing(lines, "old.go"), failing(lines, "old.go", "new.go")); !slices.Equal(r.New, []string{"new.go"}) || !slices.Equal(r.AlreadyThere, []string{"old.go"}) || r.Fixed != nil || r.NotComparable != "" {
		t.Errorf("an added occurrence: %+v", r)
	}
	if r := row(failing(lines, "old.go", "gone.go"), failing(lines, "old.go")); r.New != nil || !slices.Equal(r.Fixed, []string{"gone.go"}) {
		t.Errorf("a fixed occurrence: %+v", r)
	}
	if r := row(failing(lines, "old.go"), passing(lines)); r.New != nil || !slices.Equal(r.Fixed, []string{"old.go"}) {
		t.Errorf("a check the arm made pass: %+v", r)
	}
	if r := row(failing(boolean), failing(boolean)); r.NotComparable == "" || r.New != nil || r.Fixed != nil || r.AlreadyThere != nil {
		t.Errorf("a boolean check failing on both is not comparable: %+v", r)
	}
	if r := row(failing(lines, "old.go"), errored(lines, "timed out after 10m")); r.Fixed != nil || r.NotComparable != "on this arm: the check could not answer: timed out after 10m" {
		t.Errorf("an arm the check errored on fixes nothing: %+v", r)
	}
	if r := row(errored(lines, "exit 2"), failing(lines, "new.go")); r.New != nil || r.NotComparable != "on the base: the check could not answer: exit 2" {
		t.Errorf("a base the check errored on makes nothing new: %+v", r)
	}
	if r := row(CheckAnswer{Block: "style", Version: 1, Check: "small", Kind: "diff-rubric", State: conformance.CannotAssess.String(), Reason: conformance.NotAssessedJudgesAChange},
		failing(diff)); !slices.Equal(r.New, []string{"small"}) || r.NotComparable != "" {
		t.Errorf("a diff rubric judges the arm's own change: %+v", r)
	}
}

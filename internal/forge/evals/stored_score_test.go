package evals

import (
	"strings"
	"testing"

	"flokkit.io/forge/internal/forge/evals/internal/evalssql"
	"flokkit.io/forge/internal/ids/idstest"
)

// A terminal arm whose stored score cannot be read is no reading: it is not
// shown as an empty scored result and not tallied as a known landing.
func TestAStoredScoreThatCannotBeReadIsLeftOut(t *testing.T) {
	t.Parallel()
	bad := armOf(evalssql.EvalArm{ID: idstest.N(1), SourceChangeID: idstest.N(5), Arm: ArmVariant, State: string(ArmScored), ScoreJson: []byte(`{"landed":"unreadable"}`)}, 0, 0)
	if !bad.Score.NotComparable || !strings.Contains(bad.Score.Unreadable, "could not be read") || bad.Score.Landed {
		t.Fatalf("an unreadable score: %+v", bad.Score)
	}
	good := armOf(evalssql.EvalArm{ID: idstest.N(2), SourceChangeID: idstest.N(5), Arm: ArmBaseline, State: string(ArmScored), ScoreJson: []byte(`{"landed":true,"checks":[]}`)}, 0, 0)
	tallies, comparable, notComparable := tally([]Arm{good, bad}, nil)
	if comparable != 0 || notComparable != 1 {
		t.Fatalf("tallied %d comparable, %d not", comparable, notComparable)
	}
	for _, m := range tallies {
		if m.Better+m.Worse+m.Same != 0 {
			t.Fatalf("%s counted an unreadable arm: %+v", m.Measure, m)
		}
	}
}

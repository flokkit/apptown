package attention

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

type changedApprovals struct {
	rules []workflow.StandingRevision
	err   error
}

func (f changedApprovals) ChangedStandingApprovals(context.Context) ([]workflow.StandingRevision, error) {
	return f.rules, f.err
}

// Workflow's owner aggregates projects; Attention preserves one rule-level item.
func TestJanitorApprovalsShowsOneItemPerChangedRule(t *testing.T) {
	for _, packs := range []bool{false, true} {
		rule := workflow.StandingRevision{RuleID: idstest.N(92), Question: workflow.StandingQuestion{Definition: workflow.StandingDefinition{Categories: workflow.StandingCategories{WaysOfWorking: packs}}}}
		source := JanitorApprovals{Work: changedApprovals{rules: []workflow.StandingRevision{rule}}}
		got, err := source.Snapshot(t.Context())
		name := "Dependency updates"
		if packs {
			name = "Keep packs current"
		}
		if err != nil || !got.Known || len(got.Items) != 1 || got.Items[0].Title != name+" approval changed meaning; enable it again in Settings" || got.Items[0].Href != "/janitor/settings" || got.Items[0].Key != "rule/"+rule.RuleID.String() {
			t.Fatalf("changed approval: %+v %v", got, err)
		}
	}
	empty, err := (JanitorApprovals{Work: changedApprovals{}}).Snapshot(t.Context())
	if err != nil || !empty.Known || len(empty.Items) != 0 {
		t.Fatalf("unchanged: %+v %v", empty, err)
	}
	failure := errors.New("eligibility unavailable")
	unknown, err := (JanitorApprovals{Work: changedApprovals{err: failure}}).Snapshot(t.Context())
	if !errors.Is(err, failure) || unknown.Known {
		t.Fatalf("unknown: %+v %v", unknown, err)
	}
}

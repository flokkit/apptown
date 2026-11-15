package attention

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// JanitorApprovals reports approvals that require renewed consent.
type JanitorApprovals struct {
	Work interface {
		ChangedStandingApprovals(context.Context) ([]workflow.StandingRevision, error)
	}
}

func (s JanitorApprovals) Snapshot(ctx context.Context) (Snapshot, error) {
	out := Snapshot{Name: "Janitor approvals", Href: "/janitor/settings", Items: []Item{}}
	rules, err := s.Work.ChangedStandingApprovals(ctx)
	if err != nil {
		return out, err
	}
	out.Known = true
	for _, rule := range rules {
		out.Items = append(out.Items, Item{Key: "rule/" + rule.RuleID.String(), Severity: SeverityWarning, Title: rule.ChangedMeaningSentence(), Href: out.Href})
	}
	return out, nil
}

package attention

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/ids"
)

// Checks reads only confirmed problems from one batch of stored owner facts.
type Checks struct {
	Conformance interface {
		CurrentView(context.Context, []ids.ID) ([]conformance.ProjectStanding, error)
	}
}

func (s Checks) Snapshot(ctx context.Context, projects []ids.ID) (Snapshot, error) {
	standing, err := s.Conformance.CurrentView(ctx, projects)
	if err != nil {
		return Snapshot{}, err
	}
	out := Snapshot{Known: true}
	for _, p := range standing {
		if p.Lanes.Findings == 0 && p.Lanes.Broken == 0 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, p.JudgedAt)
		count := p.Lanes.Findings + p.Lanes.Broken
		out.Items = append(out.Items, Item{Count: count, Key: p.ProjectID.String(), Title: fmt.Sprintf("%s · app checks · %d confirmed problems", p.Name, count), Detail: fmt.Sprintf("%d confirmed problems; %d checks failed to run. Review the app checks and fix the problems.", p.Lanes.Findings, p.Lanes.Broken), Severity: SeverityWarning, Href: "/conformance/projects/" + url.PathEscape(p.Slug), ObservedAt: at})
	}
	return out, nil
}

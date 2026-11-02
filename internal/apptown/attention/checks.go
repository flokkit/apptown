package attention

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/ids"
)

// Checks renders confirmed problems and unknown attempts from stored owner classifications.
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

		at, _ := time.Parse(time.RFC3339, p.JudgedAt)
		href := "/conformance/projects/" + url.PathEscape(p.Slug)
		if count := p.Lanes.Findings; count > 0 {
			out.Items = append(out.Items, Item{Count: count, Key: p.ProjectID.String(), Title: fmt.Sprintf("%s · app checks · %d confirmed problems", p.Name, count), Detail: "Review the app checks and fix the confirmed problems.", Severity: SeverityWarning, Href: href, ObservedAt: at})
		}
		if count := len(p.NotKnown); count > 0 {
			out.Items = append(out.Items, Item{Count: count, Key: p.ProjectID.String() + "/unknown", Title: fmt.Sprintf("%s · app checks · %d not known", p.Name, count), Detail: strings.Join(p.NotKnown, "; "), Unknown: true, Severity: SeverityWarning, Href: href, ObservedAt: at})
		}
	}
	return out, nil
}

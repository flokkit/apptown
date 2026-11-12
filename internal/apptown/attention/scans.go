package attention

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
)

// Scans reports missing, stale and unavailable observations. Findings confined
// to a running image never make an attention item.
type Scans struct {
	Work     *workflow.Service
	Projects *project.Projects
}

func (s Scans) ProjectSnapshot(ctx context.Context, id ids.ID) (Snapshot, error) {
	scans, err := s.Work.LatestMainScans(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	p, err := s.Projects.ByID(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	href := "/projects/" + p.Slug + "#janitor"
	out := Snapshot{Name: "Scans", Href: href, Known: true, ObservedAt: scans.LastScan, Items: []Item{}}
	if !scans.Known {
		out.Items = append(out.Items, Item{Key: "scans/" + id.String(), Severity: SeverityWarning, Title: fmt.Sprintf("%s: scan not known", p.Name), Detail: "A scan is missing, could not run, or is older than three days.", Href: href, Unknown: true, Stale: scans.Stale, ObservedAt: scans.LastScan})
	}
	return out, nil
}

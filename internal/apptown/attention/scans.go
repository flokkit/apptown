package attention

import (
	"context"
	"fmt"
	"strings"

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
	if !scans.Known && !scans.NoRepository {
		out.Items = append(out.Items, Item{Key: "scans/" + id.String(), Severity: SeverityWarning, Title: fmt.Sprintf("%s: scan not known", p.Name), Detail: "A scan is missing, could not run, or is older than three days.", Href: href, Unknown: true, Stale: scans.Stale, ObservedAt: scans.LastScan})
	}
	queues, err := s.Work.Queues(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	for _, state := range queues {
		if state.State == "stuck" || state.State == "stopped" {
			title := fmt.Sprintf("%s: %s stuck since %s", p.Name, state.Title, state.Since.Format("2 Jan"))
			if state.State == "stopped" {
				title = fmt.Sprintf("%s: %s stopped after %d failed tries", p.Name, state.Title, state.Failed)
			}
			out.Items = append(out.Items, Item{Key: "queue/" + id.String() + "/" + state.Queue, Severity: SeverityWarning, Title: title, Detail: queueDetail(state), Href: href, ObservedAt: state.Since})
		}
	}
	return out, nil
}

func queueDetail(state workflow.QueueStatus) string {
	detail := state.Reason
	var packages []string
	for _, p := range state.Packages {
		packages = append(packages, fmt.Sprintf("%s %s → %s", p.Package, p.From, p.To))
	}
	if len(packages) > 0 {
		detail += " · moved: " + strings.Join(packages, ", ")
	}
	return detail
}

package attention

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

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
		out.Items = append(out.Items, Item{Key: "scans/" + id.String(), Severity: SeverityWarning, Title: fmt.Sprintf("%s: scan not known", p.Name), Detail: scanDetail(scans.Reason), Href: href, Unknown: true, Stale: scans.Stale, ObservedAt: scans.LastScan})
	}
	queues, err := s.Work.Queues(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	for _, state := range queues {
		if state.State == "stuck" || state.State == "stopped" {
			out.Items = append(out.Items, queueItem(id, p.Name, href, state))
		}
	}
	return out, nil
}

// queueItem links to the queue's change when it has one: the change holds the
// whole error, and the item carries its first line.
func queueItem(id ids.ID, name, href string, state workflow.QueueStatus) Item {
	if !state.ChangeID.IsZero() {
		href = "/changes/" + state.ChangeID.String()
	}
	return Item{Key: "queue/" + id.String() + "/" + state.Queue, Severity: SeverityWarning, Title: name + ": " + state.Title + " " + state.Words, Detail: queueDetail(state), Href: href, ObservedAt: state.Since}
}

// scanDetail is the owner's reason for the scans being not known, which names
// the case, and what would answer it.
func scanDetail(reason string) string {
	reason = strings.TrimRight(firstLine(reason), ".")
	if reason == "" {
		return "AppTown has no complete current scan. A new scan would answer it."
	}
	first, size := utf8.DecodeRuneInString(reason)
	return string(unicode.ToUpper(first)) + reason[size:] + ". A new scan would answer it."
}

// firstLine keeps Attention text to one line: a reason can be a whole tool
// output, and every Attention surface (the list, the top bar, webhooks and
// email) shows the detail as it is. The item's link holds the rest.
func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}

func queueDetail(state workflow.QueueStatus) string {
	detail := firstLine(state.Reason)
	var packages []string
	for _, p := range state.Packages {
		packages = append(packages, fmt.Sprintf("%s %s → %s", p.Package, p.From, p.To))
	}
	if len(packages) > 0 {
		detail += " · moved: " + strings.Join(packages, ", ")
	}
	return detail
}

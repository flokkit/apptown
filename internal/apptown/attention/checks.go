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
	out := Snapshot{Known: true, Name: "App checks", Href: "/conformance", NextStep: "Open Conformance to see each app's checks."}
	for _, p := range standing {
		at, _ := time.Parse(time.RFC3339, p.JudgedAt)
		// The source is as old as its oldest judged app; apps never judged are
		// reported by their own standing, not by this time.
		if !at.IsZero() && (out.ObservedAt.IsZero() || at.Before(out.ObservedAt)) {
			out.ObservedAt = at
		}
		href := "/conformance/projects/" + url.PathEscape(p.Slug)
		if count := p.Lanes.Findings; count > 0 {
			out.Items = append(out.Items, Item{Count: count, Key: p.ProjectID.String(), Title: fmt.Sprintf("%s · app checks · %d confirmed problems", p.Name, count), Detail: "Review the app checks and fix the confirmed problems.", Severity: SeverityWarning, Href: href, ObservedAt: at})
		}
		// The card is a summary: it counts the checks that could not run and
		// leaves each tool's own message to the app's checks page. AppTown's
		// own faults stay in words, since they name what an administrator
		// must set up and the app's page cannot fix them.
		var count int
		var faults []string
		for _, u := range p.NotKnown {
			if u.Evaluator != "llm" || u.SourceReadFailure || u.AppTownFault {
				count++
				if u.AppTownFault {
					faults = append(faults, u.Detail)
				}
			}
		}
		if count > 0 {
			checks := "checks"
			if count == 1 {
				checks = "check"
			}
			detail := fmt.Sprintf("%d %s couldn't run. Open the app's checks to see why.", count, checks)
			if len(faults) > 0 {
				detail += " " + strings.Join(faults, "; ")
			}
			out.Items = append(out.Items, Item{Count: count, Key: p.ProjectID.String() + "/unknown", Title: fmt.Sprintf("%s · app checks · %d not known", p.Name, count), Detail: detail, Unknown: true, Severity: SeverityWarning, Href: href, ObservedAt: at})
		}
	}
	return out, nil
}

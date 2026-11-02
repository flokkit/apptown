package conformance

// The conformance SURFACE's projections (design/subsystems/conformance-surface.md §3–§5):
//
//   - a LENS: current (latest verdict per adopted check, stale/not-scanned
//     overlaid) or as-of-review-N (exactly that review's verdicts, its
//     snapshotted exceptions, nothing recomputed);
//   - a project's STANDING: its checks sorted into lanes and one sentence;
//   - the DELTA between a review and the previous comparable one;
//   - a project's HISTORY across reviews;
//   - ADJUDICATIONS: a person confirming that an AI flag is real, keyed to the
//     exact verdict (never a disposition, never carried to a later verdict).
//
// Everything here reads immutable records and computes; nothing mutates.

import (
	"context"
	"fmt"

	"github.com/flokkit/apptown/internal/ids"
)

// LensInfo says what the view is a view OF.
type LensInfo struct {
	Kind   string       `json:"kind"` // current | review
	Review *Review      `json:"review,omitempty"`
	Delta  *ReviewDelta `json:"delta,omitempty"`
}

// View is the one response shape both lenses render.
type View struct {
	Lens          LensInfo                   `json:"lens"`
	Projects      []ProjectStanding          `json:"projects"`
	ByProject     map[string][]Observation   `json:"byProject"`
	ByCheck       map[string][]Observation   `json:"byCheck"`
	CheckDefs     map[string]CheckDefinition `json:"checkDefs"`
	Exceptions    map[string][]Disposition   `json:"exceptions"`
	Adjudications map[ids.ID]Adjudication    `json:"adjudications"`
	// PriorConfirmations: for a fresh AI flag, the earlier confirmed verdict of
	// the same check on the same project (sha, by, at) — so a second confirm is
	// informed, never automatic (§9 item 2).
	PriorConfirmations map[ids.ID]PriorConfirmation `json:"priorConfirmations"`
	Summary            Summary                      `json:"summary"`
	Reviews            []Review                     `json:"reviews"`
}

// View renders the surface under a lens: reviewID 0 = current.
func (s *Service) View(ctx context.Context, reviewID ids.ID) (*View, error) {
	if _, err := s.require(ctx, permission{Action: readFleet}); err != nil {
		return nil, err
	}
	store := s.records
	if reviewID.IsZero() {
		facts, err := store.current(ctx)
		if err != nil {
			return nil, err
		}
		return currentConformanceView(ctx, facts, s.sourceHead)
	}
	h, err := store.historical(ctx, reviewID)
	if err != nil {
		return nil, err
	}
	observations := RecordedObservations(h.Verdicts, h.Adjudications)
	view := &View{Lens: LensInfo{Kind: "review", Review: h.Review, Delta: h.Delta}, Adjudications: h.Adjudications, PriorConfirmations: h.PriorConfirmations,
		CheckDefs: h.CheckDefs, Reviews: h.Reviews, Exceptions: map[string][]Disposition{}, ByProject: groupByProject(observations), ByCheck: groupByCheck(observations)}
	names := map[ids.ID]string{}
	for _, p := range h.Projects {
		names[p.ID] = p.Name
	}
	slugs := map[ids.ID]string{}
	for _, v := range h.Verdicts {
		slugs[v.ProjectID] = v.ProjectSlug
	}
	for _, d := range h.Exceptions {
		slug := slugs[d.ProjectID]
		if slug == "" {
			slug = fmt.Sprintf("project-%d", d.ProjectID)
		}
		view.Exceptions[slug] = append(view.Exceptions[slug], d)
	}
	for id, slug := range slugs {
		standing := SummarizeProject(id, slug, view.ByProject[slug])
		standing.Name = displayName(names[id], slug)
		view.Projects = append(view.Projects, standing)
	}
	sortProjects(view.Projects)
	view.Summary = summarizeFleet(view.Projects, len(view.ByCheck))
	return view, nil
}

func displayName(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

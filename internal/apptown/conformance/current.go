package conformance

import (
	"context"
	"encoding/json"

	"github.com/flokkit/apptown/internal/ids"
)

func currentConformanceView(ctx context.Context, f *currentConformanceFacts, head func(context.Context, Project) (SourceHead, error)) (*View, error) {
	view := &View{Lens: LensInfo{Kind: "current"}, Exceptions: map[string][]Disposition{}, CheckDefs: map[string]CheckDefinition{},
		Adjudications: f.Adjudications, PriorConfirmations: map[ids.ID]PriorConfirmation{}, Reviews: f.Reviews}
	latest := map[CheckIdentity]Verdict{}
	recordedProjects := map[ids.ID]bool{}
	for _, v := range f.Verdicts {
		latest[v.Identity()] = v
		recordedProjects[v.ProjectID] = true
	}
	observations := []Observation{}
	for _, p := range f.Projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var source SourceHead
		if recordedProjects[p.ID] {
			var err error
			source, err = head(ctx, p)
			if err != nil {
				return nil, err
			}
		}
		active := map[ids.ID]bool{}
		for _, d := range f.Exceptions[p.ID] {
			active[d.ID] = true
		}
		if ds := f.Exceptions[p.ID]; len(ds) > 0 {
			view.Exceptions[p.Slug] = ds
		}
		for _, c := range p.Checks {
			key := CheckKey(c.Block, c.Version, c.Check.Name)
			d, ok := view.CheckDefs[key]
			if !ok {
				d = DefinitionOf(c.Block, c.Version, c.Check)
			}
			d.Adopters = append(d.Adopters, p.Slug)
			view.CheckDefs[key] = d
			ref := CheckRef{ProjectID: p.ID, ProjectSlug: p.Slug, Block: c.Block, Version: c.Version, Check: c.Check.Name}
			v, exists := latest[ref.Identity()]
			if !exists {
				observations = append(observations, Unassessed(ref))
				continue
			}
			var exception DispositionRef
			if json.Unmarshal([]byte(v.ExceptedBy), &exception) != nil {
				exception = DispositionRef{}
			}
			observed := Observe(v, CurrentContext{Check: ref, HeadSHA: source.SHA, SourceUnavailable: source.Unavailable, ExceptionActive: active[exception.ID]})
			observations = append(observations, observed)
			if LaneOf(observed, f.Adjudications) == LaneFlagged {
				if prior, ok := f.PriorConfirmations[v.ID]; ok {
					view.PriorConfirmations[v.ID] = prior
				}
			}
		}
	}
	view.ByProject = GroupByProject(observations)
	view.ByCheck = GroupByCheck(observations)
	for _, p := range f.Projects {
		standing := SummarizeProject(p.ID, p.Slug, view.ByProject[p.Slug], view.Adjudications)
		standing.Name = displayName(p.Name, p.Slug)
		view.Projects = append(view.Projects, standing)
	}
	SortProjects(view.Projects)
	view.Summary = SummarizeFleet(view.Projects, len(view.ByCheck))
	return view, nil
}

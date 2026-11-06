package model

import (
	"context"
	"sort"

	"github.com/flokkit/apptown/internal/ids"
)

// ProjectModels contains the project's assignment and the choices offered to it.
// It excludes other projects, raw mappings, consumers and instance spend.
type ProjectModels struct {
	Profile  string             `json:"profile"`
	Profiles []string           `json:"profiles"`
	Tasks    []ProjectModelTask `json:"tasks"`
}

type ProjectModelTask struct {
	Slot   string                   `json:"slot"`
	Gloss  string                   `json:"gloss"`
	Models map[string]ResolvedModel `json:"models"`
}

type ResolvedModel struct {
	Pattern string `json:"pattern"`
	Slot    string `json:"slot"`
	Profile string `json:"profile"`
}

// ForProject reads an assignment and resolves each task for every offered
// profile through the execution resolver. Callers must authorize project read.
// Read failures are returned rather than represented as an empty model.
func (s *Slots) ForProject(ctx context.Context, projectID ids.ID) (ProjectModels, error) {
	profile, err := NewSelection(s).profile(ctx, projectID)
	if err != nil {
		return ProjectModels{}, err
	}
	profiles, err := s.Profiles(ctx)
	if err != nil {
		return ProjectModels{}, err
	}
	cells, err := s.Cells(ctx)
	if err != nil {
		return ProjectModels{}, err
	}
	names := append([]string(nil), StockSlots...)
	stock := map[string]bool{}
	for _, name := range names {
		stock[name] = true
	}
	var extra []string
	for name := range cells {
		if !stock[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	names = append(names, extra...)
	out := ProjectModels{Profile: profile, Profiles: profiles, Tasks: []ProjectModelTask{}}
	for _, name := range names {
		task := ProjectModelTask{Slot: name, Gloss: Gloss[name], Models: map[string]ResolvedModel{}}
		for _, choice := range profiles {
			pattern, slot, fromProfile, err := s.resolve(ctx, choice, name)
			if err != nil {
				return ProjectModels{}, err
			}
			task.Models[choice] = ResolvedModel{Pattern: pattern, Slot: slot, Profile: fromProfile}
		}
		out.Tasks = append(out.Tasks, task)
	}
	return out, nil
}

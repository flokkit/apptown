package adoption

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
)

// PackVersion identifies the publication whose visible memberships are shown.
type PackVersion struct {
	PackID  ids.ID
	Version int64
}

// FleetProjects contains only authorized project slugs. Public pack metadata is
// still owned by Ways; empty memberships do not hide a published pack.
type FleetProjects struct {
	On    map[PackVersion][]string
	NotOn map[ids.ID][]string
}

// Fleet computes visibility once for the complete page, not once per publication.
// This is an observation, not a reusable authorization handle.
func (s *Service) Fleet(ctx context.Context) (FleetProjects, error) {
	if s.deps.Authority == nil {
		return FleetProjects{}, access.ErrAuthorityUnavailable
	}
	idList, err := s.deps.Authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return FleetProjects{}, err
	}
	return readFleet(ctx, s.db, idList)
}
func readFleet(ctx context.Context, q database.Queryer, idList []ids.ID) (FleetProjects, error) {
	out := FleetProjects{On: map[PackVersion][]string{}, NotOn: map[ids.ID][]string{}}
	records := adoptionsql.New(q.SQL())
	memberships, err := records.FleetPackMemberships(ctx, idList)
	if err != nil {
		return FleetProjects{}, err
	}
	missing, err := records.FleetMissingSelections(ctx, idList)
	if err != nil {
		return FleetProjects{}, err
	}
	for _, r := range memberships {
		key := PackVersion{PackID: r.PackID, Version: r.Version}
		out.On[key] = append(out.On[key], r.Slug)
	}
	for _, r := range missing {
		out.NotOn[r.PackID] = append(out.NotOn[r.PackID], r.Slug)
	}
	return out, nil
}

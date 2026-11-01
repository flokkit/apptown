package adoption

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/database"
)

// PackVersion identifies the publication whose visible memberships are shown.
type PackVersion struct {
	PackID  ids.ID
	Version int64
}

// fleetProjects contains only authorized project slugs. Public pack metadata is
// still owned by Ways; empty memberships do not hide a published pack.
type fleetProjects struct {
	On map[PackVersion][]string
}

// Fleet computes visibility once for the complete page, not once per publication.
// This is an observation, not a reusable authorization handle.
func (s *Service) Fleet(ctx context.Context) (fleetProjects, error) {
	if s.deps.Authority == nil {
		return fleetProjects{}, access.ErrAuthorityUnavailable
	}
	idList, err := s.deps.Authority.VisibleProjects(ctx, access.CapProjectRead)
	if err != nil {
		return fleetProjects{}, err
	}
	return readFleet(ctx, s.db, idList)
}
func readFleet(ctx context.Context, q database.Queryer, idList []ids.ID) (fleetProjects, error) {
	out := fleetProjects{On: map[PackVersion][]string{}}
	records := adoptionsql.New(q.SQL())
	memberships, err := records.FleetPackMemberships(ctx, idList)
	if err != nil {
		return fleetProjects{}, err
	}
	for _, r := range memberships {
		key := PackVersion{PackID: r.PackID, Version: r.Version}
		out.On[key] = append(out.On[key], r.Slug)
	}
	return out, nil
}

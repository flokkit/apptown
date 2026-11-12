package wow

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
)

// PackHistory is one pack as its page shows it: the author's declaration,
// every version with its lineage, and which version is recommended.
type PackHistory struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Origin      string        `json:"origin"`
	Declaration PackSpec      `json:"declaration"`
	Recommended int64         `json:"recommended"`
	Latest      int64         `json:"latest"`
	Versions    []Publication `json:"versions"`
}

// history reads a pack's page. Absence is ErrNoPack.
func (s *packs) history(ctx context.Context, name string) (*PackHistory, error) {
	p, err := packByNameIn(ctx, wowsql.New(s.db.SQL()), name)
	if err != nil {
		return nil, err
	}
	versions, err := s.publicationsOf(ctx, name)
	if err != nil {
		return nil, err
	}
	decl, _ := decodeSpec(string(p.SpecJson))
	return &PackHistory{Name: p.Name, Description: p.Description, Origin: p.Origin, Declaration: decl,
		Recommended: p.LatestVersion, Latest: p.LatestVersion, Versions: versions}, nil
}

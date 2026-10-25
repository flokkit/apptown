package verification

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/project"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/conformance"
	"github.com/flokkit/apptown/internal/database"
)

type Source struct {
	adoption *adoption.Service
	dataDir  string
}

func (s Source) Head(ctx context.Context, p conformance.ProjectSource) (conformance.SourceHead, error) {
	if err := ctx.Err(); err != nil {
		return conformance.SourceHead{}, err
	}
	repo, err := (project.RepositoryRef{ID: p.ID, Slug: p.Slug}).Path(s.dataDir)
	if err != nil {
		return conformance.SourceHead{}, err
	}
	return readSourceHead(ctx, repo, p.Branch)
}

// ChecksIn is the live fleet source; Work's gates use their admitted receipt.
func (s Source) ChecksIn(ctx context.Context, q database.Queryer, idList []ids.ID) (map[ids.ID][]conformance.DeclaredCheck, error) {
	rows, err := s.adoption.ChecksIn(ctx, q, idList)
	if err != nil {
		return nil, err
	}
	out := map[ids.ID][]conformance.DeclaredCheck{}
	for id, checks := range rows {
		for _, c := range checks {
			out[id] = append(out[id], conformance.DeclaredCheck{Block: c.Block, Version: c.Version, Check: conformance.CheckDeclaration(c.Check)})
		}
	}
	return out, nil
}

func (s Source) HistoricalChecksIn(ctx context.Context, q database.Queryer, versions []conformance.CheckVersion) ([]conformance.DeclaredCheck, error) {
	refs := make([]adoption.CheckVersion, len(versions))
	for i, v := range versions {
		refs[i] = adoption.CheckVersion{Block: v.Block, Version: v.Version}
	}
	rows, err := s.adoption.HistoricalChecksIn(ctx, q, refs)
	if err != nil {
		return nil, err
	}
	return Declarations(rows), nil
}

// NewSource binds adopted declarations and bounded committed-head observation.
func NewSource(a *adoption.Service, dataDir string) Source {
	return Source{adoption: a, dataDir: dataDir}
}

func Declarations(checks []adoption.DeclaredCheck) []conformance.DeclaredCheck {
	var out []conformance.DeclaredCheck
	for _, c := range checks {
		out = append(out, conformance.DeclaredCheck{Block: c.Block, Version: c.Version, Check: conformance.CheckDeclaration(c.Check)})
	}
	return out
}

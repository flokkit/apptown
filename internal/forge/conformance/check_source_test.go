package conformance

import (
	"context"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/ids"
)

type testCheckSource struct{}

func (testCheckSource) ChecksIn(ctx context.Context, q database.Queryer, projects []ids.ID) (map[ids.ID][]DeclaredCheck, error) {
	source, err := adoption.ChecksIn(ctx, q, projects)
	if err != nil {
		return nil, err
	}
	out := map[ids.ID][]DeclaredCheck{}
	for id, checks := range source {
		for _, c := range checks {
			out[id] = append(out[id], DeclaredCheck{Block: c.Block, Version: c.Version, Check: CheckDeclaration(c.Check)})
		}
	}
	return out, nil
}

type unavailableHeadSource struct{ testCheckSource }

func (unavailableHeadSource) Head(context.Context, ProjectSource) (SourceHead, error) {
	return SourceHead{Unavailable: ErrSource.Error()}, nil
}

func (testCheckSource) HistoricalChecksIn(ctx context.Context, q database.Queryer, versions []CheckVersion) ([]DeclaredCheck, error) {
	refs := make([]adoption.CheckVersion, len(versions))
	for i, v := range versions {
		refs[i] = adoption.CheckVersion{Block: v.Block, Version: v.Version}
	}
	rows, err := adoption.HistoricalChecksIn(ctx, q, refs)
	if err != nil {
		return nil, err
	}
	var out []DeclaredCheck
	for _, r := range rows {
		out = append(out, DeclaredCheck{Block: r.Block, Version: r.Version, Check: CheckDeclaration(r.Check)})
	}
	return out, nil
}

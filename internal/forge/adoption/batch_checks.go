package adoption

import (
	"context"
	"encoding/json"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption/internal/adoptionsql"
	"flokkit.io/forge/internal/forge/wow"
)

// ChecksIn observes live imported definitions in one caller-owned snapshot.
// Runtime gates use Checks with their recorded revision instead.
func ChecksIn(ctx context.Context, q database.Queryer, projects []ids.ID) (map[ids.ID][]DeclaredCheck, error) {
	out := map[ids.ID][]DeclaredCheck{}
	if len(projects) == 0 {
		return out, nil
	}
	rows, err := adoptionsql.New(q.SQL()).LiveChecksForProjects(ctx, projects)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.MissingDefinition {
			return nil, fmt.Errorf("import %s v%d: pinned definition is missing", row.Name, row.Version)
		}
		var declarations []CheckDeclaration
		if err := json.Unmarshal([]byte(row.Checks), &declarations); err != nil {
			return nil, fmt.Errorf("import %s v%d: %w", row.Name, row.Version, err)
		}
		for _, declaration := range declarations {
			out[row.ProjectID] = append(out[row.ProjectID], DeclaredCheck{Block: row.Name, Version: int(row.Version), Check: declaration})
		}
	}
	return out, nil
}

func (s *Service) ChecksIn(ctx context.Context, q database.Queryer, projects []ids.ID) (map[ids.ID][]DeclaredCheck, error) {
	return ChecksIn(ctx, q, projects)
}

// CheckVersion identifies optional historical evidence; it never selects latest.
type CheckVersion struct {
	Block   string
	Version int
}

func HistoricalChecksIn(ctx context.Context, q database.Queryer, versions []CheckVersion) ([]DeclaredCheck, error) {
	refs := make([]wow.BlockRef, len(versions))
	for i, v := range versions {
		refs[i] = wow.BlockRef{Name: v.Block, Version: v.Version}
	}
	rows, err := wow.CheckVersionsIn(ctx, q, refs)
	if err != nil {
		return nil, err
	}
	var out []DeclaredCheck
	for _, row := range rows {
		var declarations []CheckDeclaration
		if err := json.Unmarshal([]byte(row.ChecksJSON), &declarations); err != nil {
			return nil, err
		}
		for _, c := range declarations {
			out = append(out, DeclaredCheck{Block: row.Block, Version: row.Version, Check: c})
		}
	}
	return out, nil
}
func (s *Service) HistoricalChecksIn(ctx context.Context, q database.Queryer, versions []CheckVersion) ([]DeclaredCheck, error) {
	return HistoricalChecksIn(ctx, q, versions)
}

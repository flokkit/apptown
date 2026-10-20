package secret

import (
	"context"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/secret/internal/secretsql"
)

// BoundName is a secret a deployment had bound, by name and store: what a
// moved project's operator must set again. Never a value or a store's ref.
type BoundName struct {
	Deployment string `json:"deployment"`
	Name       string `json:"name"`
	Store      string `json:"store"`
	Generated  bool   `json:"generated,omitempty"`
}

// ExportNamesIn reads which secret names a project's deployments had bound,
// for moving the project to another AppTown. Values do not move.
func ExportNamesIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]BoundName, error) {
	rows, err := secretsql.New(q.SQL()).ExportBindingNames(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]BoundName, len(rows))
	for i, r := range rows {
		out[i] = BoundName{Deployment: r.Deployment, Name: r.Name, Store: r.Store, Generated: r.Generated != 0}
	}
	return out, nil
}

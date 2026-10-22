package wow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/database"
)

// PackSetup is the installation declared by an exact published pack. Named
// policy definitions are resolved from the current policy library, as before.
type PackSetup struct {
	Policies   []LibraryPolicy
	Extensions []string
}

func PackSetupIn(ctx context.Context, q database.Queryer, name string, version int64) (PackSetup, error) {
	publication, err := PackVersionIn(ctx, q, name, version)
	if err != nil {
		return PackSetup{}, err
	}
	var spec struct {
		Policies   []LibraryPolicy `json:"policies"`
		Extensions []string        `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(publication.SpecJSON), &spec); err != nil {
		return PackSetup{}, err
	}
	out := PackSetup{Extensions: slices.Clone(spec.Extensions)}
	for _, policy := range spec.Policies {
		if policy.Rule == "" {
			row, err := wowsql.New(q.SQL()).LibraryPolicyByName(ctx, policy.Name)
			if err != nil {
				return PackSetup{}, fmt.Errorf("policy %s is not in the library: %w", policy.Name, err)
			}
			policy.Description, policy.Rule = row.Description, row.Rule
			if err := json.Unmarshal(row.ConfigJson, &policy.With); err != nil {
				return PackSetup{}, fmt.Errorf("policy %s has invalid configuration: %w", policy.Name, err)
			}
		}
		if _, err := CheckPolicy(policy.Name, policy.Rule, policy.With); err != nil {
			return PackSetup{}, err
		}
		policy.Origin = "pack/" + name
		out.Policies = append(out.Policies, policy)
	}
	return out, nil
}

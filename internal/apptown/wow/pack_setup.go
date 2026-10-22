package wow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/database"
)

// PackSetup is the installation declared by an exact published pack: each
// policy as the publication defines it.
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
			return PackSetup{}, fmt.Errorf("%s version %d names policy %s without its definition: publish the pack again once the policy is in the library", name, version, policy.Name)
		}
		if _, err := CheckPolicy(policy.Name, policy.Rule, policy.With); err != nil {
			return PackSetup{}, err
		}
		policy.Origin = "pack/" + name
		out.Policies = append(out.Policies, policy)
	}
	return out, nil
}

package docker

import (
	"context"

	"flokkit.io/forge/internal/deploy/appcontract"
)

// ReadContractAt reads the canonical runtime declaration at an exact repository revision.
func ReadContractAt(ctx context.Context, repo, sha string) (*appcontract.Contract, error) {
	raw, err := ContractBytesAt(ctx, repo, sha)
	if err != nil {
		return nil, err
	}
	return appcontract.Parse(raw)
}

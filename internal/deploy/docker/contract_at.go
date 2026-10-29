package docker

import (
	"context"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// ReadContractAt reads the canonical runtime declaration at an exact repository revision.
func ReadContractAt(ctx context.Context, repo, sha string) (*appcontract.Contract, error) {
	raw, err := ContractFileAt(ctx, repo, sha)
	if err != nil {
		return nil, err
	}
	return appcontract.Parse(raw)
}

package docker

import (
	"context"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/gitexec"
)

// ContractPath is where the app contract lives in a repository.
const ContractPath = ".apptown/app.yaml"

// HistoricalContractPath is where the app contract lived before the AppTown
// rename. A revision recorded before `apptown upgrade` moved it (a release's
// source, a backup set's bundle) still holds it there, and history does not
// change, so readers of a recorded revision fall back to it. Nothing writes
// it.
const HistoricalContractPath = ".forge/app.yaml"

// ContractBytesAt is the app contract at an exact revision: the file at
// ContractPath, or at HistoricalContractPath when the revision has no
// contract at ContractPath.
func ContractBytesAt(ctx context.Context, repo, sha string) ([]byte, error) {
	raw, _, err := ContractFileAt(ctx, repo, sha)
	return raw, err
}

// ContractFileAt is ContractBytesAt and the path the contract was read from.
func ContractFileAt(ctx context.Context, repo, sha string) ([]byte, string, error) {
	commit, err := (deploy.SelectedSource{Repository: repo, Revision: sha}).Resolve(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, path := range []string{ContractPath, HistoricalContractPath} {
		entry, err := gitexec.Run(ctx, repo, "ls-tree", "--name-only", commit, "--", path)
		if err != nil {
			return nil, "", err
		}
		if entry == "" {
			continue
		}
		raw, err := gitexec.OutputBytes(ctx, repo, nil, "show", commit+":"+path)
		return raw, path, err
	}
	return nil, "", ErrNoContract
}

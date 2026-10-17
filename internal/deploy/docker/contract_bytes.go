package docker

import (
	"context"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/gitexec"
)

func ContractBytesAt(ctx context.Context, repo, sha string) ([]byte, error) {
	commit, err := (deploy.SelectedSource{Repository: repo, Revision: sha}).Resolve(ctx)
	if err != nil {
		return nil, err
	}
	path := ".forge/app.yaml"
	entry, err := gitexec.Run(ctx, repo, "ls-tree", "--name-only", commit, "--", path)
	if err != nil {
		return nil, err
	}
	if entry == "" {
		return nil, ErrNoContract
	}
	return gitexec.OutputBytes(ctx, repo, nil, "show", commit+":"+path)
}

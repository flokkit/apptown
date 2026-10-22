package apptown

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/gitlab"
)

type repositoryProvider struct {
	secrets *secret.Service
	dataDir string
}

func (p repositoryProvider) Client(ctx context.Context) (*gitlab.Client, error) {
	return configuredGitLab(ctx, p.secrets)
}
func (p repositoryProvider) AskpassEnv(ctx context.Context) ([]string, error) {
	client, err := p.Client(ctx)
	if err != nil {
		return nil, err
	}
	return client.AskpassEnv(p.dataDir)
}

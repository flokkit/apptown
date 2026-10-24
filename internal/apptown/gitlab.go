package apptown

import (
	"context"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/gitexec"
	"github.com/flokkit/apptown/internal/gitlab"
	"github.com/flokkit/apptown/internal/strs"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/secret"
	"github.com/flokkit/apptown/internal/database"
)

// GitLab scm adapter (G2, design/architecture/git-state.md §4-5). The instance is
// configured via secrets.env: GITLAB_URL + GITLAB_TOKEN (PAT: api +
// write_repository). Git-over-HTTPS auth uses an askpass helper so the token
// never lands in .git/config.

func configuredGitLab(ctx context.Context, secrets *secret.Service) (*gitlab.Client, error) {
	values, err := secrets.WithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading AppTown's GitLab credentials: %w", err)
	}
	return gitlab.New(values["GITLAB_URL"], values["GITLAB_TOKEN"])
}

type repositoryPublisher struct {
	secrets *secret.Service
	db      *database.DB
	dataDir string
	now     func() time.Time // AppTown's clock, which stamps a failed push's receipt
}

func (p repositoryPublisher) PushBranch(ctx context.Context, projectID ids.ID, repoDir, branch string) {
	if _, _, ok := scm.Origin(ctx, repoDir); !ok {
		return
	}
	gl, err := configuredGitLab(ctx, p.secrets)
	if err != nil {
		return
	}
	env, err := gl.AskpassEnv(p.dataDir)
	if err != nil {
		return
	}
	// The mirror follows a publication that has happened: it is pushed
	// though the caller stops, and a stop is never recorded as a failed push.
	if out, err := gitexec.RunEnv(context.WithoutCancel(ctx), repoDir, env, "push", "-u", "origin", branch); err != nil {
		audit.At(clock.Or(p.now)()).BestEffort(ctx, p.db, "apptown-system", projectID, "git.push.failed", branch, map[string]any{"error": strs.Truncate(out, 300)})
	}
}

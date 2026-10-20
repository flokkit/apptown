package forge

import (
	"context"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/gitexec"
	"flokkit.io/forge/internal/gitlab"
	"flokkit.io/forge/internal/strs"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/secret"
)

// GitLab scm adapter (G2, design/architecture/git-state.md §4-5). The instance is
// configured via secrets.env: GITLAB_URL + GITLAB_TOKEN (PAT: api +
// write_repository). Git-over-HTTPS auth uses an askpass helper so the token
// never lands in .git/config.

func configuredGitLab(ctx context.Context, secrets *secret.Service) (*gitlab.Client, error) {
	return gitlab.New(secrets.Value(ctx, "GITLAB_URL"), secrets.Value(ctx, "GITLAB_TOKEN"))
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

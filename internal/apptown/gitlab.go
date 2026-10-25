package apptown

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/clock"
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
	attachments interface {
		Push(context.Context, ids.ID, string) (scm.VerifiedRepository, error)
	}
	db  *database.DB
	now func() time.Time
}

func (p repositoryPublisher) PushBranch(ctx context.Context, projectID ids.ID, _ string, branch string) {
	// Publication is already local and durable. Its mirror cannot undo that act,
	// but every refused or failed mirror retains an explicit receipt.
	_, err := p.attachments.Push(context.WithoutCancel(ctx), projectID, branch)
	if err == nil || errors.Is(err, scm.ErrNoUpstream) {
		return
	}
	audit.At(clock.Or(p.now)()).BestEffort(ctx, p.db, "apptown-system", projectID, "git.push.failed", branch, map[string]any{"error": strs.Truncate(err.Error(), 300)})
}

package preview

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/deployment"
)

// refusesCopyPreview: a copy's previews are served on the eval domain, so
// an AppTown with none boots no preview for a copy. The refusal is
// Deployment's placement refusal for an eval copy (evals.go there).
func (s *Service) refusesCopyPreview(ctx context.Context, projectID ids.ID) error {
	purpose, err := s.env.Purpose(ctx, projectID)
	if err != nil {
		return err
	}
	if purpose.EvalDomainOnly() && s.env.Routes().EvalDomain == "" {
		return fmt.Errorf("%w: this project is an eval copy, whose previews are served only on the eval domain, and this AppTown has none: start it with -tier-domain eval=<domain>", deployment.ErrReleaseRefused)
	}
	return nil
}

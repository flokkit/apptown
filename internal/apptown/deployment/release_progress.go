package deployment

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/releasesql"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// Repeated reports of the same stage preserve its entry time, including pulls
// of several images. Terminal releases cannot be changed by late reports.
func (s *Service) recordReleaseStage(ctx context.Context, id ids.ID, stage deploy.DeployStage) error {
	_, err := releasesql.New(s.db.SQL()).RecordReleaseStage(ctx, releasesql.RecordReleaseStageParams{ID: id, Stage: stage.String(), At: s.now()})
	return err
}

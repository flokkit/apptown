package deployment

import (
	"context"
	"fmt"

	"flokkit.io/forge/internal/ids"
)

// DataCopyCheck says why the forge cannot copy the data a deployment holds
// now, "" when it can or when it holds none (data protection answers it).
// A release onto a deployment whose data cannot be copied could change that
// data with no copy to go back to, whatever the release changes: a schema
// migration changes neither needs nor dataDir. So a release asks, and is
// refused unless a person accepts releasing over uncopied data, which the
// release records (design/subsystems/deployment.md, "Releasing over data the
// forge cannot copy").
type DataCopyCheck interface {
	CannotCopy(ctx context.Context, dep *Deployment, t *Target) string
}

// uncopiedData is why a release of bundle onto dep could change data the
// forge cannot copy, "" when it could not or the person accepted it; the
// accepted words go on the release's first log line. A release of the
// bundle the deployment already runs (an operator's override redeploying it)
// brings no new code, so it is not asked.
func (s *Service) uncopiedData(ctx context.Context, dep *Deployment, t *Target, bundle ids.ID, accepted bool, actor string) (refusedWhy, note string, err error) {
	if s.copyCheck == nil {
		return "", "", nil
	}
	if !bundle.IsZero() {
		running, err := s.runningBundle(ctx, dep)
		if err != nil {
			return "", "", err
		}
		if running == bundle {
			return "", "", nil
		}
	}
	why := s.copyCheck.CannotCopy(ctx, dep, t)
	if why == "" {
		return "", "", nil
	}
	if !accepted {
		return fmt.Sprintf("AppTown cannot copy the data %s holds (%s), so this release could change it with no copy to go back to; make that data copyable, or release accepting uncopied data (acceptUncopiedData, apptown release --accept-uncopied-data)", dep.Name, why), "", nil
	}
	return "", fmt.Sprintf("; released by %s accepting that AppTown cannot copy the data %s holds: %s", actor, dep.Name, why), nil
}

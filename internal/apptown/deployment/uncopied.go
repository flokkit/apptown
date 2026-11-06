package deployment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/ids"
)

type uncopiedAcceptance struct {
	Actor      access.Actor `json:"actor"`
	At         time.Time    `json:"at"`
	Deployment ids.ID       `json:"deployment"`
	Reason     string       `json:"reason"`
}

// The exception is only the person's choice about this deployment's known
// inability to copy. A new copy problem needs a new decision.
func (s *Service) acceptsUncopied(ctx context.Context, raw *json.RawMessage, dep *Deployment, target *Target, caller access.Principal, automatic bool) bool {
	if raw == nil || automatic || s.copyCheck == nil {
		return false
	}
	var choice uncopiedAcceptance
	if json.Unmarshal(*raw, &choice) != nil {
		return false
	}
	return choice.Actor.Assents() && choice.Actor.Principal == caller.ID && choice.Deployment == dep.ID && choice.Reason != "" && choice.Reason == s.copyCheck.CannotCopy(ctx, dep, target)
}

// dataCopyCheck says why AppTown cannot copy the data a deployment holds
// now, "" when it can or when it holds none (data protection answers it).
// A release onto a deployment whose data cannot be copied could change that
// data with no copy to go back to, whatever the release changes: a schema
// migration changes neither needs nor dataDir. So a release asks, and is
// refused unless a person accepts releasing over uncopied data, which the
// release records (design/subsystems/deployment.md, "Releasing over data the
// AppTown cannot copy").
type dataCopyCheck interface {
	CannotCopy(ctx context.Context, dep *Deployment, t *Target) string
}

// uncopiedData is why a release of bundle onto dep could change data the
// AppTown cannot copy, "" when it could not or the person accepted it; the
// accepted words go on the release's first log line. Redeploying a bundle
// still executes init steps and can change data, so it requires the same choice.
func (s *Service) uncopiedData(ctx context.Context, dep *Deployment, t *Target, accepted bool, actor string) (refusedWhy, note string, err error) {
	if s.copyCheck == nil {
		return "", "", nil
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

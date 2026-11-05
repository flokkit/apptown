package secret

import (
	"context"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/ids"
)

// RecordChatAgentDelivery records an expected chat delivery before Start, then
// its outcome with the same launch invocation and digest. A pending record remains
// explicit if the process stops or the container's outcome cannot be determined.
func (a *Service) RecordChatAgentDelivery(ctx context.Context, projectID ids.ID, launch AgentLaunch, d AgentDelivery, state, container string) error {
	if len(d.Secrets) == 0 && len(d.NotGiven) == 0 {
		return nil
	}
	return audit.At(a.now()).Record(ctx, a.Vault.db, "apptown-system", projectID, "secret.agent-delivery", ids.New(a.now()).String(), map[string]any{
		"launch": launch, "profile": d.Profile, "secrets": d.Secrets, "notGiven": d.NotGiven,
		"container": container, "references": d.References, "digest": d.Digest(), "state": state,
	})
}

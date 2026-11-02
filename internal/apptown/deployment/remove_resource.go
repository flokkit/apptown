package deployment

import (
	"context"

	"github.com/flokkit/apptown/internal/fault"
)

// RemoveDockerResource removes a container or volume after the caller has
// checked claims, confirmation and authority and recorded the request.
// The host reading is marked before the Docker effect, including a failed one.
func (s *Service) RemoveDockerResource(ctx context.Context, t *Target, kind, name string) ([]byte, error) {
	if kind != "container" && kind != "volume" {
		return nil, fault.New(fault.Invalid, "choose a Docker container or volume to remove")
	}
	dk, err := s.hosts.DockerScope(ctx, t)
	if err != nil {
		return nil, err
	}
	if err := NewHostSnapshots(s.db, s.now).BeforeAct(ctx, t.ID); err != nil {
		return nil, err
	}
	if kind == "container" {
		return dk.Run(ctx, "rm", "-f", name)
	}
	return dk.Run(ctx, "volume", "rm", name)
}

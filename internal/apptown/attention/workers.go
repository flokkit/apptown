package attention

import (
	"context"
	"github.com/flokkit/apptown/internal/apptown/maintenance"
	"github.com/flokkit/apptown/internal/execution"
)

// Workers reads only cached protocol availability. Missing agent images are a
// separate readiness concern and never reported here as a lost connection.
type Workers struct {
	Observer interface {
		Snapshot(context.Context) (maintenance.WorkerSnapshot, error)
	}
}

func (s Workers) Snapshot(ctx context.Context) (Snapshot, error) {
	if s.Observer == nil {
		return Snapshot{}, nil
	}
	observed, err := s.Observer.Snapshot(ctx)
	out := Snapshot{Known: !observed.Unknown, Stale: observed.Stale, ObservedAt: observed.ObservedAt}
	if observed.Configured && observed.Availability == execution.WorkerUnavailable {
		out.Items = []Item{{Key: observed.Identity, Severity: SeverityWarning, Title: "Worker is unavailable", Detail: "AppTown could not reach the configured worker. Work that needs it cannot start until it is available.", Href: "/infrastructure", ObservedAt: observed.ObservedAt, Stale: observed.Stale || observed.Unknown}}
	}
	return out, err
}

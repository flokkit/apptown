package deployment

import (
	"testing"

	"github.com/flokkit/apptown/internal/ids"
)

// At boot a standing deployment is rebuilt only when its container is gone.
// One that exists but is stopped was stopped on purpose — the health
// controller holding a crash loop, or a person — and stays as found, which is
// also what unless-stopped promises.
func TestBootReconcileRebuildsOnlyWhatIsGone(t *testing.T) {
	t.Parallel()
	s, _, _, projectID, target := healthWorld(t)
	const container = "apptown-env-p-main"
	if _, err := s.Deployments.EnsureStanding(t.Context(), projectID, "main", target.ID, Attributes{}.WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployments.ReserveHostPort(t.Context(), projectID, "main", container, 41999); err != nil {
		t.Fatal(err)
	}
	var rebuilt []string
	rebuild := func(p ProjectDeployment) error { rebuilt = append(rebuilt, p.Name); return nil }
	for _, state := range []ContainerPresence{ContainerRunning, ContainerStopped} {
		s.BootReconcile(t.Context(), func(ids.ID, string) (ContainerPresence, error) { return state, nil }, rebuild)
	}
	if len(rebuilt) != 0 {
		t.Fatalf("a running or stopped container was rebuilt: %v", rebuilt)
	}
	s.BootReconcile(t.Context(), func(ids.ID, string) (ContainerPresence, error) { return ContainerGone, nil }, rebuild)
	if len(rebuilt) != 1 || rebuilt[0] != "main" {
		t.Fatalf("a gone container was not rebuilt: %v", rebuilt)
	}
}

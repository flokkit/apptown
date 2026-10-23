package deployment

import (
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/ids"
)

// hostNamesOf is what the deployment name of project (slug) is called on
// its host, read from its record as the owner reads it.
func hostNamesOf(t *testing.T, s *Service, project ids.ID, slug, name string) docker.Names {
	t.Helper()
	d, err := s.Deployments.ByName(t.Context(), project, name)
	if err != nil {
		t.Fatalf("reading deployment %s: %v", name, err)
	}
	n, err := d.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// names is hostNamesOf for project p's deployment in a digest world.
func (w *digestWorld) names(t *testing.T, name string) docker.Names {
	t.Helper()
	return hostNamesOf(t, w.s, w.project, "p", name)
}

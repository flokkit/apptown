package deployment

import "github.com/flokkit/apptown/internal/deploy/docker"

// HostNames is what the deployment's things on a Docker host or Portainer
// are called: by its key, or, for a deployment made before its names were
// by key and not yet moved (host_prefix forge- or apptown-), as it was made,
// so everything it made (its data above all) is found where it is. slug is
// its project's, which only the names it was made with use.
func (e *Deployment) HostNames(slug string) (docker.Names, error) {
	return docker.NamesOf(e.ID, e.HostPrefix, slug, e.Name)
}

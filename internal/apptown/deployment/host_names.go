package deployment

import "github.com/flokkit/apptown/internal/deploy/docker"

// HostNames is what the deployment's things on a Docker host or Portainer
// are called: by its key (design/subsystems/deployment.md, "Names on a
// host").
func (e *Deployment) HostNames() (docker.Names, error) {
	return docker.NamesOf(e.ID, e.Name)
}

package deployment

import "github.com/flokkit/apptown/internal/deploy/docker"

// HostNames is what the deployment's names on its host begin with, as it
// was made: a deployment made before 1.0 keeps forge- for good, so
// everything it made (its data above all) is found under those names.
func (e *Deployment) HostNames() (docker.Prefix, error) {
	return docker.ParsePrefix(e.HostPrefix)
}

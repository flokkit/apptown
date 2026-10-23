package portainer

import (
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// StackName is the stack AppTown makes on an intranet target: prefix and
// base, as StackNames gives them.
func StackName(prefix, base string) string { return prefix + base }

// StackNames are what a deployment's stack and its volume keys on Portainer
// begin with: apptown- and dep-<key>.
func StackNames(id ids.ID) (prefix, base string) {
	return "apptown-", "dep-" + deploy.HostKey(id)
}

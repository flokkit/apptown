package portainer

import (
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// StackName is the stack AppTown makes on an intranet target: prefix and
// base, as StackNames gives them.
func StackName(prefix, base string) string { return prefix + base }

// StackNames are what a deployment's stack and its volume keys on Portainer
// begin with: apptown- and dep-<key> for a deployment named by its key, or,
// for one made before its names were by key and not yet moved, its
// recorded prefix and its project's slug (REMOVE-AFTER resource move).
func StackNames(id ids.ID, recordedPrefix, slug string) (prefix, base string) {
	if recordedPrefix == "" {
		return "apptown-", "dep-" + deploy.HostKey(id)
	}
	return recordedPrefix, slug
}

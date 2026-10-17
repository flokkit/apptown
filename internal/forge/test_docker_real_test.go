//go:build integration || journey

package forge

import "flokkit.io/forge/internal/deploy/docker"

// defaultTestDocker is the real Docker CLI in the integration and journey
// tiers, so taking a change asks the real daemon what writes its folder.
func defaultTestDocker() docker.Client { return docker.CLI{} }

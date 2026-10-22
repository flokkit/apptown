//go:build !integration && !journey

package apptown

import "github.com/flokkit/apptown/internal/deploy/docker"

// defaultTestDocker is, in the default test tier, the real Docker CLI except
// that it lists no container as writing a change's folder. Every take of a
// change asks Docker that question, and this tier must not depend on what
// happens to be running on the machine (nor on a daemon for it at all, as
// in CI's default job). Tests about a writer that survives supply their own
// Docker; the integration and journey tiers ask the real daemon.
func defaultTestDocker() docker.Client { return noFolderWriters{} }

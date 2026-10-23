package docker

import (
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// Names on the host are a protocol: the vhost router turns a hostname into
// a container name, the reapers decide fate from labels and prefixes, and
// the live fleet carries these exact strings. Every name a deployment on
// the built-in host uses is built here, with its inverse.
//
// Prefix begins every name: Current. Previews and everything disposable
// are named by the id of what they serve (a change, a run, a step, a
// chat); a standing deployment by its key (Names).
type Prefix string

// Current is what everything AppTown makes on a host is named with.
const Current Prefix = "apptown-"

// keyStackPrefix begins a standing deployment's stack id: dep-<key>
// (deploy.HostKey). Not a bare id (the reaper reads one as a step's) and
// not pv… (a preview's).
const keyStackPrefix = "dep-"

// Names are what one standing deployment's things on a host are called:
// its stack id (the owner label of its services, init containers and
// network), and every name built from it under its prefix.
type Names struct{ stack string }

// ByKey names a deployment by its key: apptown-dep-<key>,
// apptown-svc-dep-<key>-<service>, and so on.
func ByKey(id ids.ID) Names {
	return Names{stack: keyStackPrefix + deploy.HostKey(id)}
}

// NamesOf is the names of a deployment from its record: by its key, which
// a deployment not yet recorded does not have.
func NamesOf(id ids.ID, deployment string) (Names, error) {
	if id.IsZero() {
		return Names{}, fmt.Errorf("deployment %s has no id to name its things by", deployment)
	}
	return ByKey(id), nil
}

// Stack is the deployment's stack id: the owner label value of its
// services, init containers and network.
func (n Names) Stack() string { return n.stack }

// App is the public app container.
func (n Names) App() string { return string(Current) + n.stack }

// Service is a need's or a non-public part's container.
func (n Names) Service(service string) string { return Current.Service(n.stack, service) }

// Init is an init container.
func (n Names) Init(name string) string { return Current.Init(n.stack, name) }

// Network is the stack's network.
func (n Names) Network() string { return Current.Network(n.stack) }

// NeedVolume is the volume a need keeps its data in.
func (n Names) NeedVolume(need string) string { return Current.NeedVolume(n.stack, need) }

// DataVolume is the app's data volume.
func (n Names) DataVolume() string { return Current.DataVolume(n.App()) }

// Service: a service container on a stack (<prefix>svc-<stack>-<service>).
func (p Prefix) Service(stackID, service string) string {
	return string(p) + "svc-" + stackID + "-" + service
}

// Network: a stack's network (<prefix>net-<stack>).
func (p Prefix) Network(stackID string) string { return string(p) + "net-" + stackID }

// Init: an init container on a stack (<prefix>init-<stack>-<name>).
func (p Prefix) Init(stackID, name string) string { return string(p) + "init-" + stackID + "-" + name }

// DataVolume: the named volume behind a container's dataDir
// (<prefix>data-<container>). A preview's dies with the preview; a
// deployment's IS the persistence.
func (p Prefix) DataVolume(container string) string { return string(p) + "data-" + container }

// NeedVolume is the named volume a standing deployment's need keeps its
// data in (<prefix>need-<stack>-<need>), made by AppTown before the need's
// container mounts it.
func (p Prefix) NeedVolume(stackID, need string) string {
	return string(p) + "need-" + stackID + "-" + need
}

// BuildTag: the local image a boot builds from a workspace's Dockerfile,
// keyed by the container it serves. The prefix marks it local: never
// pulled from a registry.
func (p Prefix) BuildTag(container string) string { return string(p) + "preview-build-" + container }

// Named says whether a name begins with the prefix AppTown names things with.
func Named(name string) bool { return strings.HasPrefix(name, string(Current)) }

// StackOfNetwork is the inverse of Network.
func StackOfNetwork(network string) (string, bool) {
	rest, ok := strings.CutPrefix(network, string(Current)+"net-")
	return rest, ok && rest != ""
}

// ContainerOfDataVolume is the inverse of DataVolume.
func ContainerOfDataVolume(volume string) (string, bool) {
	rest, ok := strings.CutPrefix(volume, string(Current)+"data-")
	return rest, ok && rest != ""
}

// IsNamedVolume says whether a volume carries a name AppTown gives a data
// or need volume.
func IsNamedVolume(volume string) bool {
	return strings.HasPrefix(volume, string(Current)+"data-") || strings.HasPrefix(volume, string(Current)+"need-")
}

// IsLocalBuildTag: an image that exists only on this daemon.
func IsLocalBuildTag(image string) bool {
	return strings.HasPrefix(image, string(Current)+"preview-build-")
}

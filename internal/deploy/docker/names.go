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
// Prefix begins every name. Everything made now is named under Current.
// Previews and everything disposable are named by the id of what they serve
// (a change, a run, a step, a chat); a standing deployment by its key
// (Names).
type Prefix string

const (
	// Current is what everything made from 1.0 on is named with.
	Current Prefix = "apptown-"
	// Before1 is the prefix of deployments made before 1.0 that have not yet
	// been moved to their names by key (deployments.host_prefix).
	//
	// REMOVE-AFTER resource move.
	Before1 Prefix = "forge-"
)

// Prefixes is every prefix a host may carry: the inverses, which read
// names off a host for a sweep or an inventory, recognise each.
var Prefixes = []Prefix{Current, Before1}

// ParsePrefix reads a recorded prefix; anything else is refused, never
// guessed.
func ParsePrefix(s string) (Prefix, error) {
	for _, p := range Prefixes {
		if string(p) == s {
			return p, nil
		}
	}
	return "", fmt.Errorf("%q is not a prefix AppTown names things with", s)
}

// keyStackPrefix begins a standing deployment's stack id: dep-<key>
// (deploy.HostKey). Not a bare id (the reaper reads one as a step's) and
// not pv… (a preview's).
const keyStackPrefix = "dep-"

// beforeStackPrefix began the stack id of a deployment made before its
// names were by key: env-<slug>-<deployment>, which two deployments could
// share (shop/eu-main and shop-eu/main).
//
// REMOVE-AFTER resource move.
const beforeStackPrefix = "env-"

// Names are what one standing deployment's things on a host are called:
// its stack id (the owner label of its services, init containers and
// network), and every name built from it under its prefix.
type Names struct {
	prefix Prefix
	stack  string
}

// ByKey names a deployment by its key: apptown-dep-<key>,
// apptown-svc-dep-<key>-<service>, and so on.
func ByKey(id ids.ID) Names {
	return Names{prefix: Current, stack: keyStackPrefix + deploy.HostKey(id)}
}

// BeforeKey names a deployment not yet moved to its names by key, as it
// was made: <prefix>env-<slug>-<deployment>.
//
// REMOVE-AFTER resource move.
func BeforeKey(prefix Prefix, slug, deployment string) Names {
	return Names{prefix: prefix, stack: beforeStackPrefix + slug + "-" + deployment}
}

// NamesOf is the names of a deployment from its record: by key when it
// records no prefix, else as it was made.
func NamesOf(id ids.ID, recordedPrefix, slug, deployment string) (Names, error) {
	if recordedPrefix == "" {
		if id.IsZero() {
			return Names{}, fmt.Errorf("deployment %s has no id to name its things by", deployment)
		}
		return ByKey(id), nil
	}
	p, err := ParsePrefix(recordedPrefix)
	if err != nil {
		return Names{}, err
	}
	return BeforeKey(p, slug, deployment), nil
}

// ByKeyNow says whether these are names by key (not a deployment still on
// the names it was made with before 1.0).
func (n Names) ByKeyNow() bool { return strings.HasPrefix(n.stack, keyStackPrefix) }

// Prefix is what every one of these names begins with.
func (n Names) Prefix() Prefix { return n.prefix }

// Stack is the deployment's stack id: the owner label value of its
// services, init containers and network.
func (n Names) Stack() string { return n.stack }

// App is the public app container.
func (n Names) App() string { return string(n.prefix) + n.stack }

// Service is a need's or a non-public part's container.
func (n Names) Service(service string) string { return n.prefix.Service(n.stack, service) }

// Init is an init container.
func (n Names) Init(name string) string { return n.prefix.Init(n.stack, name) }

// Network is the stack's network.
func (n Names) Network() string { return n.prefix.Network(n.stack) }

// NeedVolume is the volume a need keeps its data in.
func (n Names) NeedVolume(need string) string { return n.prefix.NeedVolume(n.stack, need) }

// DataVolume is the app's data volume.
func (n Names) DataVolume() string { return n.prefix.DataVolume(n.App()) }

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

// Named says whether a name begins with a prefix AppTown names things with.
func Named(name string) bool {
	for _, p := range Prefixes {
		if strings.HasPrefix(name, string(p)) {
			return true
		}
	}
	return false
}

// PrefixOf is the prefix a name AppTown gave begins with; Current for one
// that carries none of them.
func PrefixOf(name string) Prefix {
	for _, p := range Prefixes {
		if strings.HasPrefix(name, string(p)) {
			return p
		}
	}
	return Current
}

// StackOfNetwork is the inverse of Network, under any prefix.
func StackOfNetwork(network string) (string, bool) {
	for _, p := range Prefixes {
		if rest, ok := strings.CutPrefix(network, string(p)+"net-"); ok && rest != "" {
			return rest, true
		}
	}
	return "", false
}

// ContainerOfDataVolume is the inverse of DataVolume, under any prefix.
func ContainerOfDataVolume(volume string) (string, bool) {
	for _, p := range Prefixes {
		if rest, ok := strings.CutPrefix(volume, string(p)+"data-"); ok && rest != "" {
			return rest, true
		}
	}
	return "", false
}

// IsNamedVolume says whether a volume carries a name AppTown gives a data
// or need volume, under any prefix.
func IsNamedVolume(volume string) bool {
	for _, p := range Prefixes {
		if strings.HasPrefix(volume, string(p)+"data-") || strings.HasPrefix(volume, string(p)+"need-") {
			return true
		}
	}
	return false
}

// IsLocalBuildTag: an image that exists only on this daemon, under any
// prefix (a deployment made before 1.0 runs one named with its own).
func IsLocalBuildTag(image string) bool {
	for _, p := range Prefixes {
		if strings.HasPrefix(image, string(p)+"preview-build-") {
			return true
		}
	}
	return false
}

package docker

import (
	"fmt"
	"strings"
)

// Names on the host are a protocol: the vhost router turns a hostname into
// a container name, the reapers decide fate from labels and prefixes, and
// the live fleet carries these exact strings. Every name a deployment on
// the built-in host uses is built here, with its inverse.
//
// Prefix begins every name. A standing deployment keeps the prefix it was
// made with (deployments.host_prefix): one made before 1.0 carries "forge-"
// for good, so nothing it made — above all a volume holding its data — is
// ever looked up under another name. Previews and everything disposable
// use Current.
type Prefix string

const (
	// Current is what everything made from 1.0 on is named with.
	Current Prefix = "apptown-"
	// Before1 is the prefix of deployments made before 1.0, recorded on
	// their rows; their names never change.
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

const deploymentStackPrefix = "env-"

// Container: a standing deployment's app container (<prefix>env-<slug>-main).
func (p Prefix) Container(slug, env string) string {
	return string(p) + "env-" + slug + "-" + env
}

// StackID: the owner id of a standing deployment's persistent stack. It
// carries no prefix: it is a label value, and labels keep their keys.
func StackID(slug, env string) string { return deploymentStackPrefix + slug + "-" + env }

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

// OwnerDeployment reads an AppTown.owner label of the form env-<slug>-<name>.
func OwnerDeployment(owner string, slugs []string) (slug, env string, ok bool) {
	rest, isEnv := strings.CutPrefix(owner, deploymentStackPrefix)
	if !isEnv {
		return "", "", false
	}
	for _, s := range slugs {
		if e, found := strings.CutPrefix(rest, s+"-"); found && e != "" {
			return s, e, true
		}
	}
	return "", "", false
}

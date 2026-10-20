package docker

import "strings"

// Names on the host are a protocol: the vhost router turns a hostname into
// a container name, the reapers decide fate from labels and prefixes, and
// the live fleet carries these exact strings. Every name a deployment on
// the built-in host uses is built here, with its inverse.

const (
	containerPrefix       = "forge-env-"
	svcPrefix             = "forge-svc-"
	netPrefix             = "forge-net-"
	dataVolumePrefix      = "forge-data-"
	needVolumePrefix      = "forge-need-"
	initPrefix            = "forge-init-"
	buildPrefix           = "forge-preview-build-"
	deploymentStackPrefix = "env-"
)

// deploymentContainerName: a standing deployment's app container (forge-env-<slug>-main).
func ContainerName(slug, env string) string {
	return containerPrefix + slug + "-" + env
}

// deploymentStackID: the owner id of a standing deployment's persistent stack.
func StackID(slug, env string) string { return deploymentStackPrefix + slug + "-" + env }

// svcName: a service container on a stack (forge-svc-<stack>-<service>).
func ServiceName(stackID, service string) string { return svcPrefix + stackID + "-" + service }

// netName: a stack's network (forge-net-<stack>).
func NetworkName(stackID string) string { return netPrefix + stackID }

// stackOfNetwork is the inverse of netName.
func StackOfNetwork(network string) (string, bool) {
	rest, ok := strings.CutPrefix(network, netPrefix)
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// initName: an init container on a stack (forge-init-<stack>-<name>).
func InitName(stackID, name string) string { return initPrefix + stackID + "-" + name }

// dataVolumeName: the named volume behind a container's dataDir
// (forge-data-<container>). A preview's dies with the preview; an
// deployment's IS the persistence.
func DataVolumeName(container string) string { return dataVolumePrefix + container }

// NeedVolumeName is the named volume a standing deployment's need keeps
// its data in (forge-need-<stack>-<need>), made by AppTown before the
// need's container mounts it.
func NeedVolumeName(stackID, need string) string { return needVolumePrefix + stackID + "-" + need }

// containerOfDataVolume is the inverse of dataVolumeName.
func ContainerOfDataVolume(volume string) (string, bool) {
	rest, ok := strings.CutPrefix(volume, dataVolumePrefix)
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// previewBuildTag: the local image a boot builds from a workspace's
// Dockerfile, keyed by the container it serves. The prefix marks it local:
// never pulled from a registry.
func BuildTag(container string) string { return buildPrefix + container }

// isLocalBuildTag: an image that exists only on this daemon.
func IsLocalBuildTag(image string) bool { return strings.HasPrefix(image, buildPrefix) }

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

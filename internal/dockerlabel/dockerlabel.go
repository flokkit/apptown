// Package dockerlabel is the one list of the label keys AppTown puts on the
// containers, networks, volumes and images it makes, and reads back to find
// and own them. Labels outlive an AppTown build: a container started by one
// build is found, inspected and removed by the next, so the keys are a
// format. They are lowercase and dotted, and a key already out in the world
// is renamed only with a way to move what carries it.
//
// Telemetry attribute names share the apptown. prefix; they are not labels
// and are not listed here.
package dockerlabel

import "strings"

// The keys AppTown writes when it makes something, and reads to find it.
const (
	Class       = "apptown.class"       // what kind of thing it is (see the Class values)
	Owner       = "apptown.owner"       // the stack, chat or step that owns it; removal is by owner
	Created     = "apptown.created"     // when it was made (RFC 3339), for the reaper's age rules
	Expires     = "apptown.expires"     // when a task's container may be removed (RFC 3339)
	Deadline    = "apptown.deadline"    // when a check's container may be removed (Unix seconds)
	Instance    = "apptown.instance"    // which AppTown started the built-in database
	Stack       = "apptown.stack"       // a preview's stack
	Source      = "apptown.source"      // the worktree fingerprint an app container was built from
	Incarnation = "apptown.incarnation" // which boot of a preview a container is
	Writes      = "apptown.writes"      // the folder a container may write; a second writer is refused
	Step        = "apptown.step"        // the step a container works for
	Task        = "apptown.task"        // the task key a worker container runs
	Profile     = "apptown.profile"     // the execution profile a task was admitted under
	Request     = "apptown.request"     // the digest of the task request a container runs
	Worker      = "apptown.worker"      // which worker owns a check's container
	Inference   = "apptown.inference"   // a container that may reach the inference gateway
	Console     = "apptown.console"     // the chat a network, volume or daemon belongs to

	ConsoleDind   = "apptown.console.dind"   // a chat container's Docker mode, to know when to rebuild it
	ConsoleModel  = "apptown.console.model"  // a chat container's model, likewise
	ConsoleMounts = "apptown.console.mounts" // a digest of a chat container's mounts, likewise

	// Deployment is the deployment an app container runs for: its id, or the
	// number it had before the identity cutover. Drivers and ownership
	// inspection read it.
	Deployment = "apptown.deployment"
	Generation = "apptown.generation" // which generation of the deployment it runs
	Artifact   = "apptown.artifact"   // the artifact it runs
)

// For people reading `docker ps` and `docker volume ls`: which project and
// deployment a standing deployment's thing was made for, by name. Names can
// change and are not unique over time, so nothing in AppTown reads these:
// identity, ownership and removal go by the deployment's id and key
// (Deployment, Owner). They are written only through Informational.
const (
	Project        = "apptown.project"
	DeploymentName = "apptown.deployment.name"
)

// Informational is the --label arguments that tell a person which project
// and deployment a standing deployment's container, network or volume is
// for. Nothing reads them back.
func Informational(project, deployment string) []string {
	if project == "" && deployment == "" {
		return nil
	}
	return []string{"--label", Project + "=" + project, "--label", DeploymentName + "=" + deployment}
}

// The agent image's labels: the versions of the tools inside it, written by
// deploy/apptown-agent/Dockerfile and read to record what a step ran with.
const (
	PiVersion          = "apptown.pi.version"
	PiSubagentsVersion = "apptown.pi-subagents.version"
	PiLSPVersion       = "apptown.pi-lsp.version"
	GlabVersion        = "apptown.glab.version"
)

// Keys is every key AppTown writes or reads, for the test that refuses a key
// spelled outside this list.
var Keys = []string{
	Class, Owner, Created, Expires, Deadline, Instance, Stack, Source, Incarnation, Writes, Step, Task,
	Profile, Request, Worker, Inference, Console, ConsoleDind, ConsoleModel, ConsoleMounts,
	Deployment, Generation, Artifact, Project, DeploymentName,
	PiVersion, PiSubagentsVersion, PiLSPVersion, GlabVersion,
}

// ImageKeys are the keys the agent image's build writes.
var ImageKeys = []string{PiVersion, PiSubagentsVersion, PiLSPVersion, GlabVersion}

// Before1 is the key a label had before 1.0 (forge.<name>, and
// forge.environment for Deployment). Things made before 1.0 still carry
// those keys until they are moved to their new names
// (design/subsystems/deployment.md, "Names on a host"); only the readers
// that must recognise them meanwhile ask for this.
//
// REMOVE-AFTER resource move: with every deployment on live and the demo
// moved, nothing carries these keys.
func Before1(key string) string {
	if key == Deployment {
		return "forge.environment"
	}
	return "forge." + strings.TrimPrefix(key, "apptown.")
}

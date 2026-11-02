// Package dockerlabel is the one list of the label keys AppTown puts on the
// containers, networks, volumes and images it makes, and reads back to find
// and own them. Labels outlive an AppTown build: a container started by one
// build is found, inspected and removed by the next, so the keys are a
// format. They are lowercase and dotted, and a key already out in the world
// used for identity is renamed only with a way to move what carries it.
//
// Telemetry attribute names share the apptown. prefix; they are not labels
// and are not listed here.
package dockerlabel

import (
	"strings"

	"github.com/flokkit/apptown/internal/strs"
)

// The keys AppTown writes when it makes something, and reads to find it.
const (
	Class       = "apptown.class"       // what kind of thing it is (see the Class values)
	Owner       = "apptown.owner"       // the stack, chat or step that owns it; removal is by owner
	Created     = "apptown.created"     // when it was made (RFC 3339), for the reaper's age rules
	Expires     = "apptown.expires"     // when a task's container may be removed (RFC 3339)
	Deadline    = "apptown.deadline"    // when a check's container may be removed (Unix seconds)
	Instance    = "apptown.instance"    // which AppTown installation made it (its instance-id); the inventory counts only these
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
	ReadyFor    = "apptown.ready-for"   // the container run (id and start time) a readiness reader joined

	// Built marks an image AppTown built on a host's Docker, written at
	// build time; the maintenance sweep prunes only dangling images that
	// carry it. It is its own key, not a Class: a container inherits its
	// image's labels, and a class would give containers one they were
	// never given. Nothing reads it on a container.
	Built = "apptown.built"

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

// Informational keys are private so callers can only write them. No identity,
// ownership or removal decision reads the apptown.info. namespace.
const (
	infoProject    = "apptown.info.project"
	infoDeployment = "apptown.info.deployment"
	infoChange     = "apptown.info.change"
)

// Informational tells people which project and standing deployment a resource
// is for. Nothing reads these labels back.
func Informational(project, deployment string) []string {
	return informational(project, infoDeployment, deployment)
}

// PreviewInformational tells people which project and change a preview is for.
// Long titles keep a UTF-8-safe prefix of 120 bytes followed by an ellipsis.
func PreviewInformational(project, title string) []string {
	// A title on one line keeps a docker ps table one row per thing.
	return informational(project, infoChange, strs.Truncate(strings.Join(strings.Fields(title), " "), 120))
}

func informational(project, key, name string) []string {
	var args []string
	if project != "" {
		args = append(args, "--label", infoProject+"="+project)
	}
	if name != "" {
		args = append(args, "--label", key+"="+name)
	}
	return args
}

// BuiltArgs is the --label argument every image build on a host's Docker
// carries (Built).
func BuiltArgs() []string { return []string{"--label", Built + "=1"} }

// The agent image's labels: the versions of the tools inside it, written by
// deploy/apptown-agent/Dockerfile and read to record what a step ran with.
const (
	PiVersion          = "apptown.pi.version"
	PiSubagentsVersion = "apptown.pi-subagents.version"
	PiLSPVersion       = "apptown.pi-lsp.version"
	GlabVersion        = "apptown.glab.version"
)

// keys is every key AppTown writes or reads, for the test that refuses a key
// spelled outside this list.
var keys = []string{
	Class, ReadyFor, Owner, Created, Expires, Deadline, Instance, Stack, Source, Incarnation, Writes, Step, Task,
	Profile, Request, Worker, Inference, Console, Built, ConsoleDind, ConsoleModel, ConsoleMounts,
	Deployment, Generation, Artifact, infoProject, infoDeployment, infoChange,
	PiVersion, PiSubagentsVersion, PiLSPVersion, GlabVersion,
}

// imageKeys are the keys the agent image's build writes.
var imageKeys = []string{PiVersion, PiSubagentsVersion, PiLSPVersion, GlabVersion}

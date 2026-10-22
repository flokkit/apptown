// Package dockerlabel is the one list of the label keys AppTown puts on the
// containers, networks, volumes and images it makes, and reads back to find
// and own them. Labels outlive a apptown build: a container started by one
// build is found, inspected and removed by the next, so the keys are a
// format. They are lowercase and dotted, are changed only by adding, and a
// key already out in the world is never renamed without a reader for it.
//
// Telemetry attribute names share AppTown. prefix; they are not labels
// and are not listed here.
package dockerlabel

// The keys AppTown writes when it makes something, and reads to find it.
const (
	Class       = "forge.class"       // what kind of thing it is (see the Class values)
	Owner       = "forge.owner"       // the stack, chat or step that owns it; removal is by owner
	Created     = "forge.created"     // when it was made (RFC 3339), for the reaper's age rules
	Expires     = "forge.expires"     // when a task's container may be removed (RFC 3339)
	Deadline    = "forge.deadline"    // when a check's container may be removed (Unix seconds)
	Instance    = "forge.instance"    // which AppTown started the built-in database
	Stack       = "forge.stack"       // a preview's stack
	Source      = "forge.source"      // the worktree fingerprint an app container was built from
	Incarnation = "forge.incarnation" // which boot of a preview a container is
	Writes      = "forge.writes"      // the folder a container may write; a second writer is refused
	Step        = "forge.step"        // the step a container works for
	Task        = "forge.task"        // the task key a worker container runs
	Profile     = "forge.profile"     // the execution profile a task was admitted under
	Request     = "forge.request"     // the digest of the task request a container runs
	Worker      = "forge.worker"      // which worker owns a check's container
	Inference   = "forge.inference"   // a container that may reach the inference gateway
	Console     = "forge.console"     // the chat a network, volume or daemon belongs to

	ConsoleDind   = "forge.console.dind"   // a chat container's Docker mode, to know when to rebuild it
	ConsoleModel  = "forge.console.model"  // a chat container's model, likewise
	ConsoleMounts = "forge.console.mounts" // a digest of a chat container's mounts, likewise

	// Deployment is the deployment an app container runs for. Its key keeps
	// the word from before deployments were called deployments: running
	// containers carry it, and drivers and ownership inspection read it.
	Deployment = "forge.environment"
	Generation = "forge.generation" // which generation of the deployment it runs
	Artifact   = "forge.artifact"   // the artifact it runs
)

// The agent image's labels: the versions of the tools inside it, written by
// deploy/apptown-agent/Dockerfile and read to record what a step ran with.
const (
	PiVersion          = "forge.pi.version"
	PiSubagentsVersion = "forge.pi-subagents.version"
	PiLSPVersion       = "forge.pi-lsp.version"
	GlabVersion        = "forge.glab.version"
)

// Keys is every key AppTown writes or reads, for the test that refuses a key
// spelled outside this list.
var Keys = []string{
	Class, Owner, Created, Expires, Deadline, Instance, Stack, Source, Incarnation, Writes, Step, Task,
	Profile, Request, Worker, Inference, Console, ConsoleDind, ConsoleModel, ConsoleMounts,
	Deployment, Generation, Artifact,
	PiVersion, PiSubagentsVersion, PiLSPVersion, GlabVersion,
}

// ImageKeys are the keys the agent image's build writes.
var ImageKeys = []string{PiVersion, PiSubagentsVersion, PiLSPVersion, GlabVersion}

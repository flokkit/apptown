package main

import (
	"encoding/json"
	"fmt"
	"os"

	"flokkit.io/forge/internal/ids"
)

// The core loop's --json output (stock/docs/guides/cli-json.md). These types
// are the contract a script reads through 1.x: their json names are stable,
// a field may be added, none is renamed or removed. The guide's field tables
// are generated from them (TestCLIJSONIsDocumented), so the two cannot drift.
//
// --json prints one document on stdout and nothing else there: progress and
// warnings go to stderr. It never follows a run and never asks: a loop
// command returns once the forge accepted the request, and approve needs -y.
// Exit codes are those of the command without --json.

// ChangeRef names a change: its id, its number within its project, and the
// slug#number form a person types.
type ChangeRef struct {
	ID      ids.ID `json:"id" doc:"the change's UUID"`
	Number  int64  `json:"number" doc:"its number within its project"`
	Name    string `json:"name" doc:"slug#number, the form a person types"`
	Project string `json:"project" doc:"the project's slug"`
}

// RunRef names a run: its id and its number within its project.
type RunRef struct {
	ID     ids.ID `json:"id" doc:"the run's UUID"`
	Number int64  `json:"number" doc:"its number within its project"`
}

// Opened is what fix, quick, plan and build print: the change they opened
// and the run it started.
type Opened struct {
	Change   ChangeRef `json:"change" doc:"the change opened"`
	Run      RunRef    `json:"run" doc:"the run it started"`
	Workflow string    `json:"workflow" doc:"the workflow the run follows"`
}

// LandingRequested is what land prints: the landing run the forge started.
type LandingRequested struct {
	Change ChangeRef `json:"change" doc:"the change being landed"`
	Run    RunRef    `json:"run" doc:"its landing run"`
}

// ChangeDetail is what show prints.
type ChangeDetail struct {
	Change      ChangeRef   `json:"change" doc:"the change"`
	Title       string      `json:"title" doc:"its title"`
	State       string      `json:"state" doc:"proposed, in_progress, landing, shipped or abandoned"`
	ClosedAs    string      `json:"closedAs,omitempty" doc:"nothing-to-change when AppTown closed an abandoned change because its run found nothing to change; absent otherwise"`
	StateDetail string      `json:"stateDetail" doc:"AppTown's sentence for where it is"`
	Branch      string      `json:"branch" doc:"its branch, empty before its first run"`
	Runs        []RunDetail `json:"runs" doc:"its runs, newest first"`
	Commits     []Commit    `json:"commits" doc:"its commits not on main, newest first"`
	WaitingOn   *Waiting    `json:"waitingOn" doc:"the approval it waits on, or null"`
}

// RunDetail is one run of a change.
type RunDetail struct {
	Run      RunRef `json:"run" doc:"the run"`
	Workflow string `json:"workflow" doc:"the workflow it follows"`
	Role     string `json:"role" doc:"working or landing"`
	Status   string `json:"status" doc:"running, done or abandoned"`
	Stage    string `json:"stage" doc:"the stage it is in, empty when ended"`
}

// Commit is one commit of a change.
type Commit struct {
	SHA     string `json:"sha" doc:"the commit, abbreviated"`
	Subject string `json:"subject" doc:"its first line"`
}

// Waiting is the approval a change waits on.
type Waiting struct {
	Step ids.ID `json:"step" doc:"the approval step's UUID: forge approve <step>"`
}

// ChangeList is what changes prints.
type ChangeList struct {
	Changes []ChangeRow `json:"changes" doc:"each change, in board order"`
}

// ChangeRow is one change in changes.
type ChangeRow struct {
	Change ChangeRef `json:"change" doc:"the change"`
	Title  string    `json:"title" doc:"its title"`
	State  string    `json:"state" doc:"its board column: proposed, in_progress, landing, shipped or abandoned"`
	// ClosedAs is how a closed change was closed, when that is more than
	// its state (workflow.Change.ClosedAs).
	ClosedAs string `json:"closedAs,omitempty" doc:"nothing-to-change when AppTown closed an abandoned change because its run found nothing to change; absent otherwise"`
}

// WaitOutcome is what wait prints when it stops: what it waited on, how that
// stands, and the exit code it ends with.
type WaitOutcome struct {
	Target   Target  `json:"target" doc:"what was waited on"`
	Outcome  string  `json:"outcome" doc:"done or shipped (exit 0); needs_decision (exit 2: step is the approval to pass to approve); blocked (exit 1: step failed and blocks the run); ended (exit 2: a change with no run going, a person's next move); abandoned or failed (exit 1); unknown (an operation whose end is not known, exit 4)"`
	ExitCode int     `json:"exitCode" doc:"the command's exit code, as outcome says"`
	Run      *RunRef `json:"run" doc:"the run the outcome is about, or null"`
	Step     *ids.ID `json:"step" doc:"the approval waiting (needs_decision) or the failed step (blocked) when the run holds it; null otherwise, including an approval inside a called workflow (detail names it)"`
	Detail   string  `json:"detail" doc:"AppTown's sentence: the step's name, how the run ended, or the operation's detail"`
}

// Target is what a command acted on or waited on.
type Target struct {
	Kind string `json:"kind" doc:"run, change or operation"`
	ID   ids.ID `json:"id" doc:"its UUID"`
}

// Decided is what approve prints.
type Decided struct {
	Step     ids.ID `json:"step" doc:"the step decided"`
	Decision string `json:"decision" doc:"approve"`
	Status   string `json:"status" doc:"the step's state after the decision"`
}

// ReleaseRequested is what release prints.
type ReleaseRequested struct {
	Project    string `json:"project" doc:"the project's slug"`
	Deployment string `json:"deployment" doc:"the deployment named (forge release <project> <deployment>); empty for the intranet"`
	Number     int64  `json:"number" doc:"the release's number within its project: forge wait -p <project> -r <number>"`
	URL        string `json:"url" doc:"where the intranet will serve it; empty for a named deployment, which keeps its own address"`
	State      string `json:"state" doc:"the release's state when AppTown accepted it"`
}

// ComplaintFiled is what complain prints.
type ComplaintFiled struct {
	ID ids.ID `json:"id" doc:"the complaint's UUID"`
}

// ProjectCreated is what new prints.
type ProjectCreated struct {
	ID   ids.ID `json:"id" doc:"the project's UUID"`
	Slug string `json:"slug" doc:"its slug"`
	Name string `json:"name" doc:"its name"`
}

// Versions is what version prints.
type Versions struct {
	CLI   CLIVersion   `json:"cli" doc:"this CLI"`
	Forge ForgeVersion `json:"forge" doc:"the AppTown it talks to"`
}

// CLIVersion is this CLI's version.
type CLIVersion struct {
	Version  string `json:"version" doc:"its release, or dev-<commit>"`
	Protocol int    `json:"protocol" doc:"the CLI protocol it speaks"`
}

// ForgeVersion is the forge's version as its answer names it.
type ForgeVersion struct {
	URL     string `json:"url" doc:"where it was asked"`
	Reached bool   `json:"reached" doc:"whether it answered"`
	Version string `json:"version" doc:"its version, empty when not reached or not stated"`
}

// cliJSONCommands maps each core-loop command to what its --json prints, for
// the generated guide.
var cliJSONCommands = []struct {
	Commands string
	Value    any
}{
	{"new", ProjectCreated{}},
	{"fix, quick, plan, build", Opened{}},
	{"land", LandingRequested{}},
	{"show", ChangeDetail{}},
	{"changes", ChangeList{}},
	{"wait", WaitOutcome{}},
	{"approve", Decided{}},
	{"release", ReleaseRequested{}},
	{"complain", ComplaintFiled{}},
	{"version", Versions{}},
}

// jsonOut is where printJSON writes while stdout is lent to stderr
// (stdoutToStderr); nil means stdout.
var jsonOut *os.File

// stdoutToStderr sends what a command prints for a person to stderr, so
// stdout holds only the --json document, and returns the undo.
func stdoutToStderr() func() {
	prev := os.Stdout
	jsonOut, os.Stdout = prev, os.Stderr
	return func() { os.Stdout, jsonOut = prev, nil }
}

// printJSON writes v as the one document on stdout.
func printJSON(v any) error {
	out := os.Stdout
	if jsonOut != nil {
		out = jsonOut
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("writing the JSON answer: %w", err)
	}
	return nil
}

// changeRefOf names a change for --json.
func changeRefOf(project string, number int64, id ids.ID) ChangeRef {
	return ChangeRef{ID: id, Number: number, Name: changeName(project, number, id), Project: project}
}

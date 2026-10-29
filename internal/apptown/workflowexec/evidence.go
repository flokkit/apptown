package workflowexec

import "github.com/flokkit/apptown/internal/execution"

// agentEvidence is what an agent/pi step records. Keys are read by name
// elsewhere (usage accounting, the model slot report, the live feed), so
// nothing here is omitempty unless it was conditional before.
type agentEvidence struct {
	Model            string `json:"model"`
	ModelSlot        string `json:"modelSlot"`
	ModelProfile     string `json:"modelProfile"`
	PiVersion        string `json:"piVersion"`
	SteeringHash     string `json:"steeringHash"`
	ScmAccess        string `json:"scmAccess"`
	GlabVersion      string `json:"glabVersion"`
	SubagentsVersion string `json:"subagentsVersion"`
	Provider         string `json:"provider"`
	Usage            any    `json:"usage"`
	// UsageIncomplete, when present, says why the usage recorded for the
	// step may be lower than what it spent: a transcript not read whole.
	UsageIncomplete string         `json:"usageIncomplete,omitempty"`
	Output          string         `json:"output"`
	Files           []string       `json:"files"`
	DurationMs      int64          `json:"durationMs"`
	QueuedMs        int64          `json:"queuedMs"`
	Attempts        int            `json:"attempts"`
	Executor        string         `json:"executor"`
	GuidanceHash    string         `json:"guidanceHash"`
	Skills          map[string]int `json:"skills"`
	Resources       map[string]int `json:"resources"`
	Image           string         `json:"image,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	// NothingToChange: the step declared that an empty result is a complete
	// answer and left the change empty; the run ends without landing
	// (workflow.NothingToChangeEvidence reads it).
	NothingToChange bool   `json:"nothingToChange,omitempty"`
	StderrTail      string `json:"stderrTail,omitempty"`
	// Worker names where an attempt on a worker ran, Source the commit it
	// was given and Result its commits as kept in the hub.
	Worker string              `json:"worker,omitempty"`
	Source string              `json:"source,omitempty"`
	Result *taskResultEvidence `json:"result,omitempty"`
	// UsageUnknown says why no usage was recorded for an invocation: its
	// output could not be read. Unknown is never zero.
	UsageUnknown string `json:"usageUnknown,omitempty"`
	// WorkerProfile is what the worker measured, and its operator declared,
	// of the machine an attempt's private Docker or services ran on: the
	// observed facts, with no verdict on them.
	WorkerProfile *execution.WorkerProfile `json:"workerProfile,omitempty"`
}

// taskResultEvidence names a task's result as kept in the project's hub:
// its candidate ref, or its refused ref.
type taskResultEvidence struct {
	Ref     string `json:"ref"`
	Commit  string `json:"commit"`
	Refused bool   `json:"refused"`
}

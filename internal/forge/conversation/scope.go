// Package conversation owns Forge chat scope, provenance and accounting over
// the generic console runtime. Session labels have one interpretation here.
package conversation

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/console"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/workflow"
)

// What AppTown keeps on a console session's record. The console holds these
// as labels it never reads; AppTown groups by scope and reads them back.
const (
	ProjectLabel = "project" // the project's id
	SlugLabel    = "slug"    // the project's slug
	ChangeLabel  = "change"  // the change a change console works
	RunLabel     = "run"     // the lane run a resumed agent's inference is charged to
	StepLabel    = "step"
	AttemptLabel = "attempt"
	StartedLabel = "started" // the changes this conversation started, comma-separated
	// OpenerLabel is the principal of the person who opened the chat: the
	// chat acts for them, by their principal, whatever their name becomes.
	OpenerLabel = "opener"
	// PlacementLabel says where a chat runs: OnWorker for a chat on the
	// worker; absent for one on this machine. It is set when the chat opens
	// and never changes (design/operations/workers.md, "Chats on the worker").
	PlacementLabel = "placement"
)

// ImportedLabel marks a chat moved here from another AppTown: history only,
// never opened, woken or written here. Its value is the import's id.
const ImportedLabel = "imported"

// Imported reports whether a chat's labels mark it as moved from another
// forge.
func Imported(labels map[string]string) bool { return labels[ImportedLabel] != "" }

// OnWorker is PlacementLabel's value for a chat on the worker.
const OnWorker = "worker"

// OnWorkerRecord reports whether a session's record places it on the worker.
func OnWorkerRecord(meta console.Meta) bool { return meta.Label(PlacementLabel) == OnWorker }

// Key is what a session is grouped by: its change, else its project.
func Key(projectID, changeID ids.ID) string {
	if !changeID.IsZero() {
		return fmt.Sprintf("change:%d", changeID)
	}
	return fmt.Sprintf("project:%d", projectID)
}

func LabelsFor(p *project.Project, change *workflow.Change) map[string]string {
	labels := map[string]string{ProjectLabel: p.ID.String(), SlugLabel: p.Slug}
	if change != nil {
		labels[ChangeLabel] = change.ID.String()
	}
	return labels
}

func labelInt(labels map[string]string, key string) int64 {
	n, _ := strconv.ParseInt(labels[key], 10, 64)
	return n
}

// labelID reads a record id a label names; absent or unreadable is Nil.
func labelID(labels map[string]string, key string) ids.ID {
	id, _ := ids.Parse(labels[key])
	return id
}

// Started is the changes this conversation started, in order.
func Started(s *console.Session) []ids.ID {
	var idList []ids.ID
	for _, f := range strings.Split(s.Label(StartedLabel), ",") {
		if id, err := ids.Parse(f); err == nil && !id.IsZero() {
			idList = append(idList, id)
		}
	}
	return idList
}

func RecordStarted(ctx context.Context, sessions *console.Service, s *console.Session, changeID ids.ID) error {
	idList := append(Started(s), changeID)
	parts := make([]string, len(idList))
	for i, id := range idList {
		parts[i] = id.String()
	}
	return sessions.Annotate(ctx, s.ID, StartedLabel, strings.Join(parts, ","))
}

// ForProject selects every session on a project, change consoles included.
func ForProject(projectID ids.ID) console.Match {
	return console.Match{Labels: map[string]string{ProjectLabel: projectID.String()}}
}

// Scope names the project, Change and resumed producer owning a conversation.
type Scope struct {
	ProjectID ids.ID
	ChangeID  ids.ID
	RunID     ids.ID
	StepID    ids.ID
	Attempt   int64
	Slug      string
}

func FromLabels(labels map[string]string) Scope {
	return Scope{
		ProjectID: labelID(labels, ProjectLabel),
		ChangeID:  labelID(labels, ChangeLabel),
		RunID:     labelID(labels, RunLabel),
		StepID:    labelID(labels, StepLabel),
		Attempt:   labelInt(labels, AttemptLabel),
		Slug:      labels[SlugLabel],
	}
}
func Of(s *console.Session) Scope { return FromLabels(s.Labels) }

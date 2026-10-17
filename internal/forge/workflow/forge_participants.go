package workflow

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/stamp"
)

// Scoped participants for forge's own composition: each names its scope
// (a project, a run) and reads or writes Work's own tables for it.

// RunCountsByWorkflowIn is how many runs each workflow has had on a
// project, for the lanes page.
func RunCountsByWorkflowIn(ctx context.Context, q database.Queryer, project ids.ID) (map[string]int, error) {
	rows, err := workflowsql.New(q.SQL()).WorkflowRunCounts(ctx, project)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.WorkflowName] = int(r.Runs)
	}
	return out, nil
}

// PreviewStepIDsIn is a run's preview steps, for the reaper that tears
// their containers down.
func PreviewStepIDsIn(ctx context.Context, q database.Queryer, run ids.ID) ([]ids.ID, error) {
	rows, err := workflowsql.New(q.SQL()).PreviewStepIDsMeta(ctx, run)
	if err != nil {
		return nil, database.Missing(err)
	}
	if rows == nil {
		rows = []ids.ID{}
	}
	return rows, nil
}

// ScheduleLastFiredIn is when a project's scheduled workflow last fired,
// found=false before it was ever armed.
func ScheduleLastFiredIn(ctx context.Context, q database.Queryer, project ids.ID, workflow string) (stamp.Time, bool, error) {
	rows, err := workflowsql.New(q.SQL()).WorkflowScheduleByIdentity(ctx, workflowsql.WorkflowScheduleByIdentityParams{ProjectID: project, WorkflowName: workflow})
	if err != nil || len(rows) == 0 {
		return stamp.Time{}, false, err
	}
	return stamp.Time{NullTime: sql.NullTime{Time: rows[0].LastFiredAt, Valid: true}}, true, nil
}

// ArmScheduleIn records a schedule's firing time; a new schedule starts
// now and missed historical firings are not replayed.
func ArmScheduleIn(ctx context.Context, q database.Queryer, project ids.ID, workflow string, at time.Time) error {
	return workflowsql.New(q.SQL()).ArmWorkflowSchedule(ctx, workflowsql.ArmWorkflowScheduleParams{ProjectID: project, WorkflowName: workflow, LastFiredAt: at})
}

// AddArtifactIn stores one artifact on a run and returns its id. An
// artifact body is text when it is text and bytes when it is not (a file
// an agent produced, a log with a stray byte): Postgres TEXT holds only
// valid UTF-8 without NUL, and it must never be handed a mutated copy of a
// value and told the write succeeded. at is the forge's time of its making,
// which an approval brief compares with its run's step finishes.
func AddArtifactIn(ctx context.Context, q database.Queryer, run ids.ID, kind, name, ref, body string, at time.Time) (ids.ID, error) {
	sqlq := workflowsql.New(q.SQL())
	if utf8.ValidString(body) && strings.IndexByte(body, 0) < 0 {
		row, err := sqlq.InsertTextArtifact(ctx, workflowsql.InsertTextArtifactParams{ID: ids.New(at.UTC()), CreatedAt: at.UTC(), RunID: run, Kind: kind, Name: name, Ref: ref, Body: body})
		return row.ID, err
	}
	row, err := sqlq.InsertBinaryArtifact(ctx, workflowsql.InsertBinaryArtifactParams{ID: ids.New(at.UTC()), CreatedAt: at.UTC(), RunID: run, Kind: kind, Name: name, Ref: ref, BodyBytes: []byte(body)})
	return row.ID, err
}

// SlotSpendIn is the tokens burned per model slot by steps finished after
// since (a time on the forge's clock, which stamps a step's finish), from
// step evidence: input and output per slot. Workflow steps only; chat
// turns are the usage ledger's.
func SlotSpendIn(ctx context.Context, q database.Queryer, since time.Time) (map[string]map[string]int64, error) {
	rows, err := workflowsql.New(q.SQL()).SlotSpendRows(ctx, since)
	if err != nil {
		return nil, err
	}
	spend := map[string]map[string]int64{}
	for _, r := range rows {
		slot := r.ModelSlot
		if slot == "" {
			continue
		}
		if spend[slot] == nil {
			spend[slot] = map[string]int64{}
		}
		spend[slot]["input"] += r.InputTokens
		spend[slot]["output"] += r.OutputTokens
	}
	return spend, nil
}

// ReplaceSteeringArtifactIn records the final steering text and its hash after
// SCM access is added. It updates only the existing named steering artifacts.
func ReplaceSteeringArtifactIn(ctx context.Context, q database.Queryer, runID ids.ID, name, body, ref string) error {
	return workflowsql.New(q.SQL()).ReplaceSteeringArtifact(ctx, workflowsql.ReplaceSteeringArtifactParams{Body: body, Ref: ref, RunID: runID, Name: name})
}

// ScheduleOutcome is what a schedule's last recorded firing did.
type ScheduleOutcome string

const (
	// ScheduleArmed: the schedule was first seen then and has not fired since.
	ScheduleArmed ScheduleOutcome = "armed"
	// ScheduleOpened: the firing opened a change and started its run.
	ScheduleOpened ScheduleOutcome = "opened"
	// ScheduleSkipped: the previous run was still active.
	ScheduleSkipped ScheduleOutcome = "skipped"
	// ScheduleFailed: the firing's change or run could not be started.
	ScheduleFailed ScheduleOutcome = "failed"
	// ScheduleUnknown: recorded before outcomes were kept (schema 144).
	ScheduleUnknown ScheduleOutcome = "unknown"
)

// ScheduleRecord is a schedule's firing record: when it last fired (or was
// armed) and what that did. ChangeID is the change an opened firing made.
type ScheduleRecord struct {
	Workflow string
	At       time.Time
	Outcome  ScheduleOutcome
	ChangeID ids.ID
	// ChangeNumber names ChangeID within the project; 0 when none or gone.
	ChangeNumber int64
}

// RecordScheduleFiringIn advances an armed schedule, including skipped or
// failed firings, with what the firing did; changeID is the change an
// opened firing made and 0 otherwise. Arming a new schedule is a separate
// operation.
func RecordScheduleFiringIn(ctx context.Context, q database.Queryer, projectID ids.ID, workflow string, at time.Time, outcome ScheduleOutcome, changeID ids.ID) error {
	return workflowsql.New(q.SQL()).RecordWorkflowScheduleFiring(ctx, workflowsql.RecordWorkflowScheduleFiringParams{LastFiredAt: stamp.Of(at).NullTime,
		LastOutcome: string(outcome), LastChangeID: changeID, ProjectID: projectID, WorkflowName: workflow})
}

// ScheduleRecordsIn are a project's schedule records by workflow name; a
// schedule never armed has none.
func ScheduleRecordsIn(ctx context.Context, q database.Queryer, project ids.ID) (map[string]ScheduleRecord, error) {
	rows, err := workflowsql.New(q.SQL()).WorkflowSchedulesOfProject(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make(map[string]ScheduleRecord, len(rows))
	for _, r := range rows {
		out[r.WorkflowName] = ScheduleRecord{Workflow: r.WorkflowName, At: r.LastFiredAt, Outcome: ScheduleOutcome(r.LastOutcome), ChangeID: r.LastChangeID, ChangeNumber: r.LastChangeNumber}
	}
	return out, nil
}

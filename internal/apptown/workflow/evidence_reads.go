package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

const maxArtifactPreview = 16 << 10

type Event struct {
	ID      ids.ID          `json:"id"`
	RunID   ids.ID          `json:"runId"`
	At      string          `json:"at"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}
type Artifact struct {
	ID        ids.ID `json:"id"`
	RunID     ids.ID `json:"runId"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Ref       string `json:"ref"`
	Body      string `json:"body"`
	BodyBytes int64  `json:"bodyBytes,omitempty"` // original stored byte size, including binary bodies
	CreatedAt string `json:"createdAt"`
	Binary    bool   `json:"binary,omitempty"`
}
type ArtifactBody struct {
	Artifact
	Raw []byte
}
type workflowArtifactView uint8

const (
	artifactPreview workflowArtifactView = iota + 1
	artifactText
	artifactBody
)

type workflowArtifactQuery struct {
	RunID, ID ids.ID
	View      workflowArtifactView
}
type ReadView uint8

const (
	DetailView ReadView = iota + 1
	TraceView
)

// One detached Work snapshot serves detail, trace and internal callers. Definition
// is available for composition without adding another field to the detail payload.
// A read never gives its returned Steps authority to finish a runner attempt.
type RunSnapshot struct {
	Run         *Run          `json:"run"`
	Definition  *schema.Spec  `json:"-"`
	Stages      []string      `json:"stages"`
	Steps       []*Step       `json:"steps"`
	State       ActivityState `json:"state,omitempty"`
	StateDetail string        `json:"stateDetail,omitempty"`
	// Ending is why an abandoned run ended (runEnding); empty for any
	// other run.
	Ending    string     `json:"ending,omitempty"`
	Events    []Event    `json:"events"`
	Artifacts []Artifact `json:"artifacts"`
	// LandingMeans is what the landing's approvals would ask, shown beside a
	// waiting acceptance: accepting counts for them where the rules let the
	// accepter approve them. Empty for any other run.
	LandingMeans []string `json:"landingMeans"`
	// LandingQueued: the run is done work whose landing AppTown has queued
	// and not yet started; the landing starts on its own.
	LandingQueued bool `json:"landingQueued,omitempty"`
	// Next is what a person can do on the run now, each with its step
	// (nextActs); empty while AppTown has the next move or the run ended.
	Next []NextAct `json:"next"`
}

func (q queries) scopedSnapshot(ctx context.Context, id ids.ID, view ReadView, project ids.ID) (*RunSnapshot, error) {
	if view != DetailView && view != TraceView {
		return nil, fmt.Errorf("invalid Work snapshot view")
	}
	tx, err := q.db.ReadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := readWorkflowSnapshot(ctx, tx, id, view, q.wait, project, q.automatic)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return snapshot, nil
}
func readWorkflowSnapshot(ctx context.Context, db database.Queryer, id ids.ID, view ReadView, wait SlotWait, project ids.ID, automatic func(string) bool) (*RunSnapshot, error) {
	run, err := readScopedRun(ctx, db, id, project)
	if err != nil {
		return nil, err
	}
	spec, err := run.Definition()
	if err != nil {
		return nil, err
	}
	steps, err := readSteps(ctx, db, id)
	if err != nil {
		return nil, err
	}
	for _, step := range steps {
		if carriesCheckEvidence(step.Type) {
			step.CheckReading = readingOf(step.Evidence)
		}
	}
	out := &RunSnapshot{Run: run, Definition: spec, Stages: spec.StageNames(), Steps: steps, Artifacts: []Artifact{}, Next: []NextAct{}}
	if view == DetailView && (run.Status == RunRunning || run.Status == RunAbandoned) {
		activity, err := readWorkflowSnapshotActivity(ctx, db, run, steps, wait)
		if err != nil {
			return nil, err
		}
		out.State, out.StateDetail = activity.State, activity.Detail
		runnable := func(kind string) bool { return assertionKind(kind) || automatic != nil && automatic(kind) }
		out.Next = nextActs(run, steps, activity, runnable, spec.Role == "landing")
		if run.Status == RunAbandoned {
			out.Ending = runEnding(activity.Detail, run, steps)
		}
	}
	// Accepted work is done before its landing starts; until it does, the
	// change is not waiting on anyone (wait once called it a person's move).
	if view == DetailView && run.Status == RunDone && run.ParentRunID.IsZero() && !run.ChangeID.IsZero() && spec.Role == "working" {
		queued, err := workflowsql.New(db.SQL()).LandingQueued(ctx, workflowsql.LandingQueuedParams{WorkRunID: run.ID, ChangeState: ChangeInProgress.String()})
		if err != nil {
			return nil, err
		}
		if queued {
			out.State, out.StateDetail, out.LandingQueued = ActivityQueued, "landing", true
		}
	}
	out.Events, err = readWorkflowEvents(ctx, db, id, view)
	if err != nil {
		return nil, err
	}
	artifactView := artifactText
	if view == DetailView {
		artifactView = artifactPreview
	}
	artifacts, err := readWorkflowArtifacts(ctx, db, workflowArtifactQuery{RunID: id, View: artifactView})
	if err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		out.Artifacts = append(out.Artifacts, artifact.Artifact)
	}
	return out, nil
}

// runEnding is why an abandoned run ended, in the record's own words: the
// abandonment's reason and, when a failed step in the stage it ended in gave
// one, that step's reason, which says what to do next (a landing that found
// its branch behind main names the catch-up). apptown wait and the change's
// chat read it here rather than guess why the work stopped: a chat that
// guesses can go on saying its landing awaits approval after main moved and
// the landing ended.
func runEnding(reason string, run *Run, steps []*Step) string {
	for _, step := range steps {
		if step.Stage != run.CurrentStage || step.OverrideBy != "" ||
			(step.Status != StatusFail && step.Status != StatusError && step.Status != StatusNeedsHuman) {
			continue
		}
		var evidence struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(step.Evidence, &evidence) != nil || strings.TrimSpace(evidence.Reason) == "" {
			continue
		}
		failed := step.Name + " (" + step.Status.String() + "): " + strings.TrimSpace(evidence.Reason)
		if reason == "" {
			return failed
		}
		return reason + "; " + failed
	}
	return reason
}

// Most detail reads already have every fact required to classify activity. Only
// a running child call needs a graph query, in this same database snapshot.
func readWorkflowSnapshotActivity(ctx context.Context, db database.Queryer, run *Run, steps []*Step, wait SlotWait) (Activity, error) {
	if run.Status == RunAbandoned {
		reason, err := workflowsql.New(db.SQL()).RunAbandonmentReason(ctx, run.ID)
		return Activity{State: ActivityAbandoned, Detail: reason}, err
	}
	node := &workflowActivityRun{id: run.ID, projectID: run.ProjectID, state: run.Status, parentID: run.ParentRunID, parentStepID: run.ParentStepID, parentAttempt: run.ParentStepAttempt}
	for _, step := range steps {
		if step.Stage != run.CurrentStage {
			continue
		}
		if step.Type == "workflow/call" && step.Status == StatusRunning {
			graph, err := readWorkflowActivity(ctx, db, []ids.ID{run.ID})
			if err != nil {
				return Activity{}, err
			}
			return graph.classify(run.ID, wait, make(map[ids.ID]bool))
		}
		node.steps = append(node.steps, workflowActivityStep{id: step.ID, attempt: step.Attempt, name: step.Name, kind: step.Type, state: step.Status, override: step.OverrideBy, finished: step.FinishedAt.NullTime})
	}
	// Only a landing's approval can be waiting for an acceptance it has yet
	// to take; other runs spend no read on it.
	if spec, err := run.Definition(); err == nil && spec.Role == "landing" && run.Status == RunRunning {
		awaiting, err := workflowsql.New(db.SQL()).LandingAwaitingAcceptance(ctx, run.ID)
		if err != nil {
			return Activity{}, err
		}
		node.awaitingAcceptance = awaiting
	}
	activity, err := (workflowActivityGraph{run.ID: node}).classify(run.ID, wait, make(map[ids.ID]bool))
	if err != nil {
		return Activity{}, err
	}
	return judgedByGateInSnapshot(ctx, db, run, steps, activity)
}
func readWorkflowEvents(ctx context.Context, db database.Queryer, runID ids.ID, view ReadView) ([]Event, error) {
	var limit int32
	if view == DetailView {
		limit = 200
	}
	rows, err := workflowsql.New(db.SQL()).ReadRunEvents(ctx, workflowsql.ReadRunEventsParams{RunID: runID, NewestFirst: view == DetailView, EventLimit: limit})
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, Event{row.ID, row.RunID, row.At.Format(time.RFC3339Nano), row.Kind, rawJSON(string(row.PayloadJson))})
	}
	return events, nil
}
func (s *RunSnapshot) PinnedGuidance() json.RawMessage {
	var latest Event
	for _, event := range s.Events {
		if event.Kind == "guidance.pinned" && event.ID.Compare(latest.ID) > 0 {
			latest = event
		}
	}
	return latest.Payload
}
func readWorkflowArtifacts(ctx context.Context, db database.Queryer, query workflowArtifactQuery) ([]ArtifactBody, error) {
	if query.RunID.IsZero() && query.ID.IsZero() {
		return nil, fmt.Errorf("artifact reads require a run or artifact identity")
	}
	var preview int32
	switch query.View {
	case artifactPreview:
		preview = maxArtifactPreview
	case artifactText, artifactBody:
	default:
		return nil, fmt.Errorf("invalid artifact view")
	}
	rows, err := workflowsql.New(db.SQL()).ReadArtifacts(ctx, workflowsql.ReadArtifactsParams{RunID: query.RunID, ArtifactID: query.ID, PreviewChars: preview, IncludeBinary: query.View == artifactBody})
	if err != nil {
		return nil, err
	}
	artifacts := make([]ArtifactBody, 0, len(rows))
	for _, row := range rows {
		if row.Binary && row.Body != "" {
			return nil, fmt.Errorf("artifact %d has both text and binary bodies", row.ID)
		}
		body := row.Body
		if query.View == artifactPreview && len(body) > maxArtifactPreview {
			end := maxArtifactPreview
			for !utf8.RuneStart(body[end]) {
				end--
			}
			body = body[:end]
		}
		artifacts = append(artifacts, ArtifactBody{Artifact: Artifact{ID: row.ID, RunID: row.RunID, Kind: row.Kind, Name: row.Name, Ref: row.Ref, Body: body, BodyBytes: row.BodySize, CreatedAt: row.CreatedAt.Format(time.RFC3339Nano), Binary: row.Binary}, Raw: row.RawBody})
	}
	return artifacts, nil
}

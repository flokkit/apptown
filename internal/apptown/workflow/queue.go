package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
)

const queueStopAfter = 3

type QueueStatus struct {
	Title        string     `json:"title"`
	Queue        string     `json:"queue"`
	State        string     `json:"state"`
	Words        string     `json:"words"`
	Since        time.Time  `json:"since"`
	Failed       int        `json:"failed"`
	Reason       string     `json:"reason"`
	Packages     []fix.Move `json:"packages"`
	ChangeID     ids.ID     `json:"changeId,omitzero"`
	ChangeNumber int64      `json:"changeNumber"`
}

// Stopped reports whether this queue’s episode has exhausted its attempts.
func (q QueueStatus) Stopped() bool { return q.Failed >= queueStopAfter }

// QueueState derives the current episode from retained attempts. Quiet scans and
// duplicate proposals neither fail an episode nor end it; landed work and a
// person’s skip or takeover end it.
func (s *Service) QueueState(ctx context.Context, project ids.ID, queue string) (QueueStatus, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: project}); err != nil {
		return QueueStatus{}, err
	}
	return queueStateIn(ctx, s.db, project, queue)
}
func queueStateIn(ctx context.Context, db database.Queryer, project ids.ID, queue string) (QueueStatus, error) {
	out := QueueStatus{Queue: queue, State: "up_to_date", Packages: []fix.Move{}}
	rows, err := workflowsql.New(db.SQL()).QueueAttempts(ctx, workflowsql.QueueAttemptsParams{ProjectID: project, Queue: queue})
	if err != nil {
		return out, err
	}
	currentFailed := false
	for _, r := range rows {
		if r.Outcome == "shipped" || r.Outcome == "skipped" {
			break
		}
		if !r.ActiveChangeID.IsZero() {
			ch, err := workflowsql.New(db.SQL()).ChangeByID(ctx, workflowsql.ChangeByIDParams{ID: r.ActiveChangeID, ProjectID: project})
			if err != nil {
				return out, err
			}
			// Promotion records a person's takeover. Its retained failure is
			// history, just like a skipped attempt, rather than this episode.
			if !ch.Automated {
				break
			}
		}
		if r.FailedAttempt {
			out.Failed++
			out.Since = r.OpenedAt
		}
		if out.Reason == "" && r.Outcome == "held" {
			out.Reason = r.Reason
			if err := json.Unmarshal(r.Packages, &out.Packages); err != nil {
				return out, err
			}
		}
		if out.ChangeID.IsZero() && !r.EndedAt.Valid && !r.ActiveChangeID.IsZero() {
			out.ChangeID = r.ActiveChangeID
			currentFailed = r.FailedAttempt
		}
	}
	if out.Failed > 0 {
		out.State = "stuck"
	}
	if out.Stopped() {
		out.State = "stopped"
	}
	if !out.ChangeID.IsZero() {
		ch, err := readChangeInProject(ctx, db, out.ChangeID, project)
		if err != nil {
			return out, err
		}
		out.ChangeNumber = ch.Number
		runs, err := workflowsql.New(db.SQL()).ChangeRuns(ctx, workflowsql.ChangeRunsParams{ProjectID: project, ChangeID: out.ChangeID})
		if err != nil {
			return out, err
		}
		active, waiting := false, false
		for _, run := range runs {
			if run.Status != RunRunning.String() {
				continue
			}
			steps, err := readStageSteps(ctx, db, run.ID, run.CurrentStage)
			if err != nil {
				return out, err
			}
			for _, step := range steps {
				if step.Status == StatusRunning && step.Type != "workflow/call" || step.Type == "agent/pi" && step.Status == StatusPending {
					active = true
				}
				if step.Type == "approval" && step.Status == StatusPending {
					ready, err := dispatchReady(ctx, db, step)
					if err != nil {
						return out, err
					}
					waiting = waiting || ready
				}
			}
		}
		if active {
			out.State = "updating"
		} else if waiting && !currentFailed && !out.Stopped() {
			out.State = "waiting"
		}
	}
	out.Words = queueWords(out)
	return out, nil
}

func queueWords(q QueueStatus) string {
	switch q.State {
	case "updating":
		return "updating"
	case "waiting":
		return "waiting for a person"
	case "stuck":
		return "stuck since " + q.Since.UTC().Format("2 Jan")
	case "stopped":
		return fmt.Sprintf("stopped after %d failed tries", q.Failed)
	default:
		return "up to date"
	}
}

// StartFix records the attempted work at the first declared command, not at a
// quiet observing firing. Targets remain after its Change is purged.
func (e *Execution) StartFix(targets []check.Finding) error {
	raw, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	if targets == nil {
		raw = []byte(`[]`)
	}
	return e.InAttempt(e.step.Type, func(tx *database.Tx, _ int64) error {
		ctx := e.ctx
		return workflowsql.New(tx.SQL()).StartQueueAttempt(ctx, workflowsql.StartQueueAttemptParams{ID: e.run.ChangeID, At: e.now(), Targets: raw})
	})
}

// KeepProposal fingerprints the independently observed candidate before tests.
// Only the latest held or skipped proposal suppresses work. Its history and
// failed count stay intact when a later quiet firing proposes the same thing.
func (e *Execution) KeepProposal(main string, listing json.RawMessage, moved []fix.Move) (bool, string, error) {
	revision, err := adoption.ByIDIn(e.ctx, e.service.db, e.run.AdoptionID)
	if err != nil {
		return false, "", err
	}
	var inputs map[string]json.RawMessage
	if err := json.Unmarshal([]byte(e.run.InputsJSON), &inputs); err != nil {
		return false, "", err
	}
	body, err := json.Marshal(struct {
		Main     string
		Adoption any
		Listing  json.RawMessage
		Targets  json.RawMessage
	}{main, revision, listing, inputs["targets"]})
	if err != nil {
		return false, "", err
	}
	hash := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(hash[:])
	return e.keepProposal(fingerprint, moved)
}

// KeepPackProposal uses main and the assessed adoption candidate, independent
// of this attempt's newly allocated target identity.
func (e *Execution) KeepPackProposal(main, candidate string) (bool, string, error) {
	return e.keepProposal(fix.ProposalFingerprint(main, candidate), nil)
}

func (e *Execution) keepProposal(fingerprint string, moved []fix.Move) (bool, string, error) {
	packages, err := json.Marshal(moved)
	if err != nil {
		return false, "", err
	}
	if moved == nil {
		packages = []byte(`[]`)
	}
	same, why := false, ""
	err = e.InAttempt(e.step.Type, func(tx *database.Tx, _ int64) error {
		ctx := e.ctx
		q := workflowsql.New(tx.SQL())
		prev, err := q.PreviousHeldAttempt(ctx, workflowsql.PreviousHeldAttemptParams{ProjectID: e.run.ProjectID, ChangeID: e.run.ChangeID})
		if err != nil {
			return err
		}
		if len(prev) > 0 && prev[0].Fingerprint == fingerprint {
			same = true
			why = "same as the try of " + prev[0].OpenedAt.Format("2 Jan")
		}
		return q.RecordQueueProposal(ctx, workflowsql.RecordQueueProposalParams{ID: e.run.ChangeID, Fingerprint: fingerprint, Packages: packages})
	})
	if err == nil && !same {
		err = e.replaceUntouched()
	}
	return same, why, err
}

// StopDuplicate closes only this untouched automated proposal; it never claims
// that its candidate was tested or changes the previous held attempt.
func (e *Execution) StopDuplicate(reason string) error {
	rows, err := workflowsql.New(e.service.db.SQL()).QueueAttempt(e.ctx, e.run.ChangeID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("duplicate proposal is not queue work")
	}
	_, err = e.service.abandonments.abandon(e.ctx, Abandonment{untouchedOnly: true, ChangeID: e.run.ChangeID, Who: "apptown-system", Reason: reason}, e.run.ProjectID)
	return err
}

// SkipUpdate is a person's explicit disposal of the queue's open work. Its
// fingerprint continues to suppress that update until a different proposal.
func (s *Service) SkipUpdate(ctx context.Context, project ids.ID, queue string, expectedChange ids.ID) error {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: project}); err != nil {
		return err
	}
	state, err := s.QueueState(ctx, project, queue)
	if err != nil {
		return err
	}
	if expectedChange.IsZero() {
		return fault.New(fault.Invalid, "name the Change to skip")
	}
	if state.ChangeID != expectedChange {
		return fault.New(fault.Conflict, "the queue has a different update; read it again before skipping")
	}
	if state.ChangeID.IsZero() {
		return fmt.Errorf("this queue has no open update")
	}
	if _, _, err := s.changeScope(ctx, state.ChangeID, actionCloseChange); err != nil {
		return err
	}
	scope, err := s.PrepareStop(ctx, project)
	if err != nil {
		return err
	}
	defer scope.Close()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		if _, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, project); err != nil {
			return err
		}
		current, err := queueStateIn(ctx, tx, project, queue)
		if err != nil {
			return err
		}
		if current.ChangeID != expectedChange {
			return fault.New(fault.Conflict, "the queue has a different update; read it again before skipping")
		}
		if _, err := scope.AbandonIn(ctx, tx, StopWork{ChangeID: state.ChangeID, Reason: "Skipped this update"}); err != nil {
			return err
		}
		return workflowsql.New(tx.SQL()).SkipQueueAttempt(ctx, state.ChangeID)
	})
}

// replaceUntouched closes earlier disposable work only after a distinct proposal
// exists. A person's touched work remains ordinary work and is never replaced.
func (e *Execution) replaceUntouched() error {
	return e.service.replaceUntouched(e.ctx, e.run)
}

func (s *Service) replaceUntouched(ctx context.Context, run *Run) error {
	rows, err := workflowsql.New(s.db.SQL()).QueueAttempt(ctx, run.ChangeID)
	if err != nil || len(rows) == 0 {
		return err
	}
	history, err := workflowsql.New(s.db.SQL()).QueueAttempts(ctx, workflowsql.QueueAttemptsParams{ProjectID: run.ProjectID, Queue: rows[0].Queue})
	if err != nil {
		return err
	}
	for _, r := range history {
		if r.ID == run.ChangeID || r.EndedAt.Valid || r.ActiveChangeID.IsZero() {
			continue
		}
		ch, err := readChangeInProject(ctx, s.db, r.ActiveChangeID, run.ProjectID)
		if err != nil {
			return err
		}
		if ch.State.Closed() {
			continue
		}
		if _, err := s.abandonments.abandon(ctx, Abandonment{untouchedOnly: true, ChangeID: ch.ID, Who: "apptown-system", Reason: "Replaced by a newer update"}, run.ProjectID); err != nil {
			return err
		}
	}
	return nil
}

// IsQueue reports whether this admitted execution belongs to disposable queue work.
func (e *Execution) IsQueue() (bool, error) {
	return workflowsql.New(e.service.db.SQL()).IsAutomatedQueueChange(e.ctx, e.run.ChangeID)
}

// Queues lists standing queues in adoption order, with their authored titles.
// An unreadable adopted definition fails the read because its queues are unknown.
func (s *Service) Queues(ctx context.Context, project ids.ID) ([]QueueStatus, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: project}); err != nil {
		return nil, err
	}
	definitions, err := s.adoptedProcedures(ctx, project)
	if err != nil {
		return nil, err
	}
	for _, d := range definitions {
		if d.problem != "" {
			return nil, fmt.Errorf("workflow %q: %s", d.name, d.problem)
		}
	}
	return s.queues(ctx, project, definitions)
}

func (s *Service) queues(ctx context.Context, project ids.ID, definitions []adoptedProcedure) ([]QueueStatus, error) {
	out := []QueueStatus{}
	seen := map[string]bool{}
	for _, d := range definitions {
		if d.doc == nil {
			continue
		}
		trigger, ok := d.doc.Standing()
		if !ok || trigger.Queue == "" || seen[trigger.Queue] {
			continue
		}
		seen[trigger.Queue] = true
		state, err := queueStateIn(ctx, s.db, project, trigger.Queue)
		if err != nil {
			return nil, err
		}
		state.Title = d.doc.Metadata.DisplayTitle()
		out = append(out, state)
	}
	return out, nil
}

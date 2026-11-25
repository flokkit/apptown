package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/fix"
	"github.com/flokkit/apptown/internal/ids"
)

const queueStopAfter = 3

// PackageMove records an independently observed dependency, image or toolchain move.
type PackageMove struct {
	Ecosystem string `json:"ecosystem"`
	Package   string `json:"package"`
	From      string `json:"from"`
	To        string `json:"to"`
}

type queueDecision struct {
	Act    NextActKind `json:"act"`
	StepID ids.ID      `json:"stepId"`
}

type QueueStatus struct {
	Workflow      string        `json:"workflow"`
	ChangeSummary string        `json:"changeSummary"`
	Title         string        `json:"title"`
	Queue         string        `json:"queue"`
	State         string        `json:"state"`
	Words         string        `json:"words"`
	StateSince    time.Time     `json:"stateSince"`
	Since         time.Time     `json:"since"`
	Failed        int           `json:"failed"`
	Reason        string        `json:"reason"`
	Files         []fix.File    `json:"files"`
	Packages      []PackageMove `json:"packages"`
	ChangeID      ids.ID        `json:"changeId,omitzero"`
	ChangeNumber  int64         `json:"changeNumber"`

	Decide *queueDecision `json:"decide,omitempty"`
}

// Stopped reports whether this queue’s episode has exhausted its attempts.
func (q QueueStatus) Stopped() bool { return q.Failed >= queueStopAfter }

// StoredQueue reads the owner’s recorded conclusion for one queue.
func (s *Service) StoredQueue(ctx context.Context, project ids.ID, queue string) (QueueStatus, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: project}); err != nil {
		return QueueStatus{}, err
	}
	return storedQueueIn(ctx, s.db, project, queue)
}

func storedQueueIn(ctx context.Context, db database.Queryer, project ids.ID, queue string) (QueueStatus, error) {
	queues, err := storedQueuesIn(ctx, db, project)
	if err != nil {
		return QueueStatus{}, err
	}
	for _, q := range queues {
		if q.Queue == queue {
			return q, nil
		}
	}
	return QueueStatus{Queue: queue, State: "unknown"}, nil
}
func queueStateIn(ctx context.Context, db database.Queryer, project ids.ID, queue string) (QueueStatus, error) {
	out := QueueStatus{Queue: queue, State: "up_to_date", Files: []fix.File{}, Packages: []PackageMove{}}
	rows, err := queueEpisodeIn(ctx, db, project, queue)
	if err != nil {
		return out, err
	}
	currentFailed, currentPromoted := false, false
	currentOutcome := ""
	for _, r := range rows {
		promoted := r.promoted
		if r.FailedAttempt && !promoted {
			out.Failed++
			out.Since = r.OpenedAt
		}
		if out.Reason == "" && (r.Outcome == "held" || r.Outcome == "faulted") {
			out.Reason = r.Reason
			if err := json.Unmarshal(r.Files, &out.Files); err != nil {
				return out, err
			}
			if err := json.Unmarshal(r.Packages, &out.Packages); err != nil {
				return out, err
			}
		}
		if (out.ChangeID.IsZero() || promoted) && !r.EndedAt.Valid && !r.ActiveChangeID.IsZero() {
			out.ChangeID = r.ActiveChangeID
			currentFailed = r.FailedAttempt && !promoted
			currentPromoted = promoted
			currentOutcome = r.Outcome
		}
	}
	if out.Failed > 0 {
		out.State = "retrying"
	}
	if currentOutcome == "held" {
		out.State = "decide"
	}
	if out.Stopped() {
		out.State = "stopped"
	}
	// Janitor cannot replace or retry a person's Change. Its unresolved
	// checks belong to that person even when AppTown caused the fault.
	if currentPromoted && (currentOutcome == "held" || currentOutcome == "faulted") {
		out.State = "decide"
	}

	if !out.ChangeID.IsZero() {
		ch, err := readChangeInProject(ctx, db, out.ChangeID, project)
		if err != nil {
			return out, err
		}
		out.ChangeNumber = ch.Number
		if ch.Type == "uplift" {
			var target adoption.Candidate
			if err := json.Unmarshal([]byte(ch.TargetCandidateJSON), &target); err != nil {
				return out, err
			}
			opened, err := time.Parse(time.RFC3339Nano, ch.CreatedAt)
			if err != nil {
				return out, err
			}
			picks, err := changedAdoptionPacksIn(ctx, db, project, opened, target)
			if err != nil {
				return out, err
			}
			names := make([]string, 0, len(picks))
			for _, pick := range picks {
				names = append(names, pick.Pack)
			}
			out.ChangeSummary = "Updates ways of working: " + strings.Join(names, ", ")
		}
		for _, r := range rows {
			if r.ActiveChangeID != out.ChangeID {
				continue
			}
			if err := json.Unmarshal(r.Files, &out.Files); err != nil {
				return out, err
			}
			if err := json.Unmarshal(r.Packages, &out.Packages); err != nil {
				return out, err
			}
			if ch.Type != "uplift" && r.Fingerprint != "" && len(out.Files) == 0 {
				out.ChangeSummary = "Makes no change"
			}
			break
		}

		runs, err := workflowsql.New(db.SQL()).ChangeRuns(ctx, workflowsql.ChangeRunsParams{ProjectID: project, ChangeID: out.ChangeID})
		if err != nil {
			return out, err
		}
		queued, err := oldestQueuedAcceptedWork(ctx, db, project, out.ChangeID)
		if err != nil {
			return out, err
		}
		landing := !queued.IsZero()
		active := false
		for _, run := range runs {
			if run.Status != RunRunning.String() {
				continue
			}
			definition, err := runRecord(run, nil)
			if err != nil {
				return out, err
			}
			spec, err := definition.Definition()
			if err != nil {
				return out, err
			}
			if run.ParentRunID.IsZero() && spec.Role == "landing" {
				landing = true
			}
			steps, err := readStageSteps(ctx, db, run.ID, run.CurrentStage)
			if err != nil {
				return out, err
			}
			decision, words, err := queueDecisionIn(ctx, db, definition, steps)
			if err != nil {
				return out, err
			}
			if decision != nil && (out.Decide == nil || decision.Act == NextApprove) {
				out.Decide, out.Words = decision, words
			}
			for _, step := range steps {
				if step.Status == StatusRunning && step.Type != "workflow/call" || step.Type == "agent/pi" && step.Status == StatusPending {
					active = true
				}
			}
		}
		if landing && !currentFailed {
			out.State = "landing"
		} else if active {
			out.State = "updating"
		} else if out.Decide != nil && out.Decide.Act == NextApprove && !currentFailed && (!out.Stopped() || currentPromoted) {
			out.State = "decide"
		}
		if out.State != "decide" {
			out.Decide, out.Words = nil, ""
		}
	}
	out.Words = queueWords(out)
	return out, nil
}

type queueEpisodeAttempt struct {
	workflowsql.AutomatedChange
	promoted bool
}

// queueEpisodeIn owns the boundaries shared by status and target suppression.
// The final row can be open person-touched work, which stays visible but is
// never counted as an automatic retry or replaced by Janitor.
func queueEpisodeIn(ctx context.Context, db database.Queryer, project ids.ID, queue string) ([]queueEpisodeAttempt, error) {
	q := workflowsql.New(db.SQL())
	rows, err := q.QueueAttempts(ctx, workflowsql.QueueAttemptsParams{ProjectID: project, Queue: queue})
	if err != nil {
		return nil, err
	}
	var episode []queueEpisodeAttempt
	for _, row := range rows {
		if row.Outcome == "shipped" || row.Outcome == "skipped" || row.Outcome == "retried" {
			break
		}
		promoted := false
		if !row.ActiveChangeID.IsZero() {
			ch, err := q.ChangeByID(ctx, workflowsql.ChangeByIDParams{ID: row.ActiveChangeID, ProjectID: project})
			if err != nil {
				return nil, err
			}
			promoted = !ch.Automated && ch.State != ChangeLanding.String()
			if promoted && ch.State != ChangeInProgress.String() {
				break
			}
		}
		episode = append(episode, queueEpisodeAttempt{AutomatedChange: row, promoted: promoted})
		if promoted {
			break
		}
	}
	return episode, nil
}

func queueDecisionIn(ctx context.Context, db database.Queryer, run *Run, steps []*Step) (*queueDecision, string, error) {
	activity, err := readWorkflowSnapshotActivity(ctx, db, run, steps, nil)
	if err != nil {
		return nil, "", err
	}
	var decision *queueDecision
	words := ""
	for _, act := range nextActs(run, steps, activity, nil, false) {
		if act.Act == NextApprove || act.Act == nextOverride && decision == nil {
			decision = &queueDecision{Act: act.Act, StepID: act.StepID}
			for _, step := range steps {
				if step.ID == act.StepID {
					if act.Act == NextApprove {
						words = "Waiting for your acceptance"
					} else {
						words = decisionReason(step)
					}
				}
			}
		}
	}
	return decision, words, nil
}

func queueWords(q QueueStatus) string {
	switch q.State {
	case "landing":
		return "waiting to land"
	case "updating":
		return "updating"
	case "decide":
		if q.Words != "" {
			return q.Words
		}
		return "Waiting for a decision on the change"
	case "retrying":
		return "trying again"
	case "stopped":
		return "AppTown could not finish this update"
	default:
		return "up to date"
	}
}

var evidenceBrackets = regexp.MustCompile(`\[[^\]]*\]`)

func decisionReason(step *Step) string {
	reason := strings.TrimSpace(evidenceReason(step.Evidence))
	// Raw structured evidence is detail, not an explanatory sentence.
	if json.Valid([]byte(reason)) {
		reason = ""
	}
	reason = strings.Join(strings.Fields(evidenceBrackets.ReplaceAllString(reason, "")), " ")
	if strings.IndexFunc(reason, unicode.IsLetter) >= 0 {
		for i, r := range reason {
			if (r == '.' || r == '!' || r == '?') && (i+1 == len(reason) || reason[i+1] == ' ') {
				reason = reason[:i+1]
				break
			}
		}
		runes := []rune(reason)
		runes[0] = unicode.ToUpper(runes[0])
		return string(runes)
	}
	if step.Status == StatusError {
		return "A check could not finish"
	}
	return "A check did not pass"
}

// StartFix records the attempted work at the producer session, not at a
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
		if err := workflowsql.New(tx.SQL()).StartQueueAttempt(ctx, workflowsql.StartQueueAttemptParams{ID: e.run.ChangeID, At: e.now(), Targets: raw}); err != nil {
			return err
		}
		return recordJanitorChangeIn(ctx, tx, e.run.ChangeID, e.now())
	})
}

// KeepProposal fingerprints the independently observed candidate before tests.
// Only the latest held or skipped proposal suppresses work. Its history and
// failed count stay intact when a later quiet firing proposes the same thing.
func (e *Execution) KeepProposal(main string, tree string, files []fix.File) (bool, string, error) {
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
		Tree     string
		Targets  json.RawMessage
	}{main, revision, tree, inputs["targets"]})
	if err != nil {
		return false, "", err
	}
	hash := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(hash[:])
	return e.keepProposal(fingerprint, files)
}

// KeepPackProposal uses main and the assessed adoption candidate, independent
// of this attempt's newly allocated target identity.
func (e *Execution) KeepPackProposal(main, candidate string) (bool, string, error) {
	return e.keepProposal(fix.ProposalFingerprint(main, candidate), nil)
}

func (e *Execution) keepProposal(fingerprint string, files []fix.File) (bool, string, error) {
	raw, err := json.Marshal(files)
	if err != nil {
		return false, "", err
	}
	if files == nil {
		raw = []byte(`[]`)
	}
	same, why := false, ""
	err = e.InAttempt(e.step.Type, func(tx *database.Tx, _ int64) error {
		ctx := e.ctx
		q := workflowsql.New(tx.SQL())
		current, err := q.QueueAttempt(ctx, e.run.ChangeID)
		if err != nil {
			return err
		}
		if len(current) == 0 || current[0].Fingerprint != "" {
			return nil
		}
		prev, err := q.PreviousHeldAttempt(ctx, workflowsql.PreviousHeldAttemptParams{ProjectID: e.run.ProjectID, ChangeID: e.run.ChangeID})
		if err != nil {
			return err
		}
		if len(prev) > 0 && prev[0].Fingerprint == fingerprint {
			same = true
			why = "same as the try of " + prev[0].OpenedAt.Format("2 Jan")
		}
		if err := q.RecordQueueProposal(ctx, workflowsql.RecordQueueProposalParams{ID: e.run.ChangeID, Fingerprint: fingerprint, Files: raw}); err != nil {
			return err
		}
		return recordJanitorChangeIn(ctx, tx, e.run.ChangeID, e.now())
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
	state, err := s.StoredQueue(ctx, project, queue)
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
		current, err := storedQueueIn(ctx, tx, project, queue)
		if err != nil {
			return err
		}
		if current.ChangeID != expectedChange {
			return fault.New(fault.Conflict, "the queue has a different update; read it again before skipping")
		}
		if _, err := scope.AbandonIn(ctx, tx, StopWork{ChangeID: state.ChangeID, Reason: "Skipped this update"}); err != nil {
			return err
		}
		if err := workflowsql.New(tx.SQL()).SkipQueueAttempt(ctx, state.ChangeID); err != nil {
			return err
		}
		return recordJanitorChangeIn(ctx, tx, state.ChangeID, s.now())
	})
}

// TryQueueAgain ends the current automated episode without suppressing its
// proposals. Closing open work requires the caller's current close authority.
func (s *Service) TryQueueAgain(ctx context.Context, project ids.ID, queue string, expectedChange ids.ID) error {
	actor, err := s.MayOpenWork(ctx, project)
	if err != nil {
		return err
	}
	state, err := s.StoredQueue(ctx, project, queue)
	if err != nil {
		return err
	}
	if state.ChangeID != expectedChange {
		return fault.New(fault.Conflict, "the queue has a different update; read it again before trying again")
	}
	if !expectedChange.IsZero() {
		if _, _, err := s.changeScope(ctx, expectedChange, actionCloseChange); err != nil {
			return err
		}
	}
	scope, err := s.PrepareStop(ctx, project)
	if err != nil {
		return err
	}
	defer scope.Close()
	return s.db.InTx(ctx, func(tx *database.Tx) error {
		q := workflowsql.New(tx.SQL())
		if _, err := q.ProjectForUpdate(ctx, project); err != nil {
			return err
		}
		current, err := storedQueueIn(ctx, tx, project, queue)
		if err != nil {
			return err
		}
		if current.ChangeID != expectedChange {
			return fault.New(fault.Conflict, "the queue has a different update; read it again before trying again")
		}
		rows, err := queueEpisodeIn(ctx, tx, project, queue)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.promoted {
				break
			}
			if row.Outcome == "held" || row.Outcome == "faulted" || row.ID == expectedChange {
				if err := q.RetryQueueAttempt(ctx, row.ID); err != nil {
					return err
				}
				if err := recordJanitorChangeIn(ctx, tx, row.ID, s.now()); err != nil {
					return err
				}
			}
		}
		if !expectedChange.IsZero() {
			if _, err := scope.AbandonIn(ctx, tx, StopWork{ChangeID: expectedChange, Reason: "Closed to try again"}); err != nil {
				return err
			}
		}
		return audit.At(s.now()).RecordIn(ctx, tx, actor, project, "queue.try-again", queue, map[string]any{"changeId": expectedChange})
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
	history, err := queueEpisodeIn(ctx, s.db, run.ProjectID, rows[0].Queue)
	if err != nil {
		return err
	}
	for _, r := range history {
		if r.ID == run.ChangeID || r.EndedAt.Valid || r.ActiveChangeID.IsZero() {
			continue
		}
		if r.promoted {
			break
		}
		if _, err := s.abandonments.abandon(ctx, Abandonment{untouchedOnly: true, ChangeID: r.ActiveChangeID, Who: "apptown-system", Reason: "Replaced by a newer update"}, run.ProjectID); err != nil {
			return err
		}
	}
	return nil
}

// IsQueue reports whether this admitted execution belongs to disposable queue work.
func (e *Execution) IsQueue() (bool, error) {
	return workflowsql.New(e.service.db.SQL()).IsAutomatedQueueChange(e.ctx, e.run.ChangeID)
}

// StoredQueues reads standing queues in adoption order, with their authored titles.
// An unreadable adopted definition fails the read because its queues are unknown.
func (s *Service) StoredQueues(ctx context.Context, project ids.ID) ([]QueueStatus, error) {
	if _, err := s.require(ctx, operationPermission{Action: actionReadProject, ProjectID: project}); err != nil {
		return nil, err
	}
	return storedQueuesIn(ctx, s.db, project)
}
func storedQueuesIn(ctx context.Context, db database.Queryer, project ids.ID) ([]QueueStatus, error) {
	rows, err := workflowsql.New(db.SQL()).JanitorRecord(ctx, project)
	if err != nil {
		return nil, err
	}
	out := []QueueStatus{}
	if len(rows) == 0 {
		return out, nil
	}
	if rows[0].Problem != "" {
		return nil, fmt.Errorf("%s", rows[0].Problem)
	}
	err = json.Unmarshal(rows[0].Queues, &out)
	return out, err
}

// RecordPackageMoves records this attempt's observations and names the update from
// those facts. A human pin remains authoritative, including an identical title.
func (e *Execution) RecordPackageMoves(moves []PackageMove) error {
	if moves == nil {
		moves = []PackageMove{}
	}
	raw, err := json.Marshal(moves)
	if err != nil {
		return err
	}
	return e.InAttempt(e.step.Type, func(tx *database.Tx, _ int64) error {
		ctx := e.ctx
		if err := workflowsql.New(tx.SQL()).RecordPackageMoves(ctx, workflowsql.RecordPackageMovesParams{ID: e.run.ChangeID, ProjectID: e.run.ProjectID, Packages: raw}); err != nil {
			return err
		}
		if len(moves) > 0 {
			names := []string{}
			seen := map[string]bool{}
			for _, m := range moves {
				if !seen[m.Package] {
					seen[m.Package] = true
					names = append(names, m.Package)
				}
			}
			words := "Update " + strings.Join(names[:min(3, len(names))], ", ")
			if len(names) > 3 {
				words += fmt.Sprintf(" and %d more", len(names)-3)
			}
			if _, err := editChangeTitleIn(ctx, tx, e.run.ProjectID, e.run.ChangeID, changeTitleEdit{act: titleFromFacts, words: words}); err != nil {
				return err
			}
		}
		return recordJanitorChangeIn(ctx, tx, e.run.ChangeID, e.now())
	})
}

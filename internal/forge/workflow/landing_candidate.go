package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/scm"
	"flokkit.io/forge/internal/strs"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
	"flokkit.io/forge/internal/gitexec"
)

// A landing checks and merges one pinned commit
// (stock/docs/guides/changes-and-main.md, "Landing: already caught up, then
// the gates"). The candidate is the change-branch commit every source gate checks; the target
// is the default-branch commit it was caught up with. The merge happens only
// while the branch is still at the candidate and the default branch still at
// the target, so the merged tree is exactly the checked one.
type LandingCandidate struct {
	Candidate string `json:"candidate"`
	Target    string `json:"target"`
	By        string `json:"by"`
	// Swept: admission committed uncommitted workspace files into the
	// candidate, work nobody accepted.
	Swept bool `json:"swept,omitempty"`
}

const (
	landingCandidateEvent   = "landing.candidate"
	landingPinRequiredEvent = "landing.pin.required"
)

// ErrLandingUnpinned: a landing admitted under candidate pinning has no
// candidate. Its gates and merge are refused; it never falls back to checking
// or merging the live workspace.
var ErrLandingUnpinned = errors.New("this landing has no pinned candidate yet; its checks and merge wait for one")

// candidateGates are the step types that check the change's source against
// the candidate. Each records the candidate it checked in its evidence, and
// a merge requires every one that passed to name the pinned candidate.
var candidateGates = []string{"tests", "scan", "guidance/conformance", "script"}

// unboundGates read the change's source but cannot be bound to a candidate.
// A checked landing that passed one is refused: its result says nothing
// about the commit being merged. A called workflow is one of them: its
// steps run as another run, which is not bound to the caller's candidate.
var unboundGates = []string{"agent/pi", "agent/review", "workflow/call"}

// LandingPinRequired marks a landing admitted under candidate pinning, as one
// of its start events. Without a pin such a run's gates and merge are
// refused; only runs admitted before pinning lack the marker.
func LandingPinRequired() StartEvent {
	return StartEvent{Kind: landingPinRequiredEvent, Payload: json.RawMessage(`{}`)}
}

// LandingCandidateStart pins the candidate a landing is admitted with, as one
// of the run's start events.
func LandingCandidateStart(c LandingCandidate) (StartEvent, error) {
	body, err := json.Marshal(c)
	return StartEvent{Kind: landingCandidateEvent, Payload: body}, err
}

// LandingCandidateIn returns the run's pinned candidate. A run without one is
// not a landing, or was admitted before candidates were pinned; a landing
// that requires a pin and has none is ErrLandingUnpinned.
//
// A workflow a landing calls checks the same candidate: its root landing's.
func LandingCandidateIn(ctx context.Context, q database.Queryer, runID ids.ID) (LandingCandidate, bool, error) {
	root, err := workflowsql.New(q.SQL()).RunRoot(ctx, workflowsql.RunRootParams{RunID: runID, MaxDepth: MaxCallDepth})
	if err != nil {
		return LandingCandidate{}, false, database.QueryError(err, "reading a run's root")
	}
	runID = root
	row, err := workflowsql.New(q.SQL()).LatestRunEventOfKind(ctx, workflowsql.LatestRunEventOfKindParams{RunID: runID, Kind: landingCandidateEvent})
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := workflowsql.New(q.SQL()).LatestRunEventOfKind(ctx, workflowsql.LatestRunEventOfKindParams{RunID: runID, Kind: landingPinRequiredEvent}); err == nil {
			return LandingCandidate{}, false, ErrLandingUnpinned
		} else if !errors.Is(err, sql.ErrNoRows) {
			return LandingCandidate{}, false, err
		}
		return LandingCandidate{}, false, nil
	}
	if err != nil {
		return LandingCandidate{}, false, err
	}
	var c LandingCandidate
	if err := json.Unmarshal(row.PayloadJson, &c); err != nil {
		return LandingCandidate{}, false, fmt.Errorf("run %d has an unreadable landing candidate: %w", runID, err)
	}
	return c, c.Candidate != "", nil
}

// PinLandingCandidate records the candidate the running step produced (the
// landing's catch-up with the default branch). A candidate different from the
// one before is a new candidate: every source gate that already ran in this
// stage checked the old one, so it is reset and runs again. A retry of the
// same candidate resets nothing.
func (e *Execution) PinLandingCandidate(c LandingCandidate) (reset []string, err error) {
	if e.claim == nil || e.finished {
		return nil, ErrStepAttemptLost
	}
	if c.Candidate == "" || c.Target == "" {
		return nil, fmt.Errorf("a landing candidate needs its commit and target")
	}
	c.By = e.claim.Name
	return database.Transact(e.ctx, e.service.db, func(tx *database.Tx) ([]string, error) {
		run, current, err := lockStep(e.ctx, tx, e.claim.ID)
		if err != nil {
			return nil, err
		}
		if current.Status != StatusRunning || current.Attempt != e.claim.OwnedAttempt() {
			return nil, ErrStepAttemptLost
		}
		if !run.ParentRunID.IsZero() {
			return nil, fmt.Errorf("a landing's candidate is pinned by the landing itself, not by a workflow it calls")
		}
		previous, pinned, err := LandingCandidateIn(e.ctx, tx, run.ID)
		if err != nil {
			return nil, err
		}
		if err := recordReceiptIn(e.ctx, tx, run.ID, landingCandidateEvent, c, e.now()); err != nil {
			return nil, err
		}
		if pinned && previous.Candidate == c.Candidate && previous.Target == c.Target {
			return nil, nil
		}
		names, err := resetForCandidateIn(e.ctx, tx, run, current.ID, c, e.now())
		if err != nil {
			return nil, err
		}
		if len(names) > 0 {
			if err := invalidateRunDecisionIn(e.ctx, tx, run, e.service.now()); err != nil {
				return nil, err
			}
		}
		return names, nil
	})
}

// resetForCandidateIn puts back to pending, anywhere in the run, every result
// that named an earlier candidate: source gates that ran (passed, failed,
// errored or overridden: an override applies to the candidate it was made
// on) and approvals that were decided or had votes. Fresh assent and fresh
// checks follow the new candidate.
func resetForCandidateIn(ctx context.Context, tx *database.Tx, run *Run, pinning ids.ID, c LandingCandidate, at time.Time) ([]string, error) {
	steps, err := stepRecords(workflowsql.New(tx.SQL()).RunSteps(ctx, workflowsql.RunStepsParams{RunID: run.ID}))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, step := range steps {
		if step.ID == pinning || step.Status == StatusRunning {
			continue
		}
		gate := slices.Contains(candidateGates, step.Type) && (step.Status != StatusPending || step.OverrideBy != "")
		approval := step.Type == "approval" && (step.Status != StatusPending || hasVotes(step))
		if !gate && !approval {
			continue
		}
		if _, err := step.Status.Transition(MoveStepReset, StatusPending); err != nil {
			return nil, err
		}
		if err := requireAttemptWrite(workflowsql.New(tx.SQL()).InvalidateStep(ctx, workflowsql.InvalidateStepParams{ID: step.ID, Attempt: step.Attempt})); err != nil {
			return nil, err
		}
		if err := recordReceiptIn(ctx, tx, run.ID, "step.recheck", landingRecheckReceipt{Step: step.Name, StepID: step.ID, Stage: step.Stage, By: "new landing candidate " + strs.ShortSHA(c.Candidate), Attempt: step.Attempt, PreviousStatus: step.Status, PreviousEvidence: step.Evidence}, at); err != nil {
			return nil, err
		}
		names = append(names, step.Name)
	}
	return names, nil
}

func hasVotes(step *Step) bool {
	document, err := readApprovalDocument(step.Evidence)
	return err != nil || len(document.evidence.Decisions) > 0
}

// LandingCandidateRefusal says why a landing may not merge its candidate yet:
// a source gate, override or approval that names another commit or none, a
// passed check that cannot be bound to a candidate, or an approval the run
// left behind undecided. Empty means everything the merge rests on names the
// pinned candidate.
func LandingCandidateRefusal(ctx context.Context, q database.Queryer, runID ids.ID, c LandingCandidate) (string, error) {
	run, err := readRun(ctx, q, runID)
	if err != nil {
		return "", err
	}
	spec, err := run.Definition()
	if err != nil {
		return "", err
	}
	stageIndex := map[string]int{}
	for i, stage := range spec.Stages {
		stageIndex[stage.Name] = i
	}
	steps, err := stepRecords(workflowsql.New(q.SQL()).RunSteps(ctx, workflowsql.RunStepsParams{RunID: runID}))
	if err != nil {
		return "", err
	}
	for _, step := range steps {
		if slices.Contains(unboundGates, step.Type) && step.Status == StatusPass {
			if step.Type == "workflow/call" {
				return fmt.Sprintf("step %s ran another workflow before the merge; its checks are not bound to the landing's candidate, so checked landings call workflows only after the merge", step.Name), nil
			}
			return fmt.Sprintf("step %s (%s) read the change's source but cannot be bound to the landing's candidate; checked landings support tests, scans, guidance conformance and scripts", step.Name, step.Type), nil
		}
		earlier := stageIndex[step.Stage] < stageIndex[run.CurrentStage]
		if earlier && slices.Contains(candidateGates, step.Type) && step.Status != StatusPass && step.Status != StatusSkipped {
			// A new candidate put this earlier check back to pending, and
			// nothing runs an earlier stage again.
			return fmt.Sprintf("check %s in stage %s has not checked the landing's candidate %s; restart the landing to check it again", step.Name, step.Stage, strs.ShortSHA(c.Candidate)), nil
		}
		if step.Type == "approval" {
			if step.Status != StatusPass {
				if earlier {
					return fmt.Sprintf("approval %s must be given again for the landing's candidate %s; restart the landing", step.Name, strs.ShortSHA(c.Candidate)), nil
				}
				continue
			}
			document, err := readApprovalDocument(step.Evidence)
			if err != nil {
				return "", err
			}
			if document.evidence.Candidate != c.Candidate {
				return fmt.Sprintf("approval %s was given for %s, not the landing's candidate %s", step.Name, orUnrecorded(document.evidence.Candidate), strs.ShortSHA(c.Candidate)), nil
			}
			continue
		}
		if !slices.Contains(candidateGates, step.Type) || step.Status != StatusPass {
			continue
		}
		var evidence struct {
			Candidate string `json:"candidate"`
		}
		_ = json.Unmarshal(step.Evidence, &evidence)
		if evidence.Candidate != c.Candidate {
			return fmt.Sprintf("gate %s checked %s, not the landing's candidate %s; recheck the landing", step.Name, orUnrecorded(evidence.Candidate), strs.ShortSHA(c.Candidate)), nil
		}
	}
	return "", nil
}

func orUnrecorded(sha string) string {
	if sha == "" {
		return "an unrecorded commit"
	}
	return strs.ShortSHA(sha)
}

// carryRefusalIn says why an acceptance given on the working run cannot count
// as approval of this landing's candidate: the acceptance was given on one
// commit, so it carries only to a landing that merges exactly that commit.
// Work published after the acceptance, swept in at admission, or brought in
// by catching up makes a different candidate, and a person approves it
// afresh. Empty when it may carry; a run with no pinned candidate is not
// judged here.
func carryRefusalIn(ctx context.Context, q database.Queryer, runID ids.ID, accepted string) (string, error) {
	latest, pinned, err := LandingCandidateIn(ctx, q, runID)
	if err != nil || !pinned {
		return "", err
	}
	switch {
	case accepted == "":
		return "the acceptance did not record which commit it accepted", nil
	case latest.Swept:
		return "the landing swept in work that was not part of the acceptance", nil
	case latest.Candidate != accepted:
		return fmt.Sprintf("the landing's candidate %s is not the accepted commit %s", strs.ShortSHA(latest.Candidate), strs.ShortSHA(accepted)), nil
	}
	return "", nil
}

// landingPins pins a landing that admission could not pin (a fresh change
// whose branch did not exist yet, or a landing with no change) once its
// workspace is prepared: the workspace's branch is published as the landing
// run and pinned with the default branch. A branch with nothing on it pins
// the default branch itself: there is nothing to land.
type landingPins struct {
	db      *database.DB
	dataDir string
	clocked
}

// acceptedTip is the commit a person accepting the run's work accepts: its
// branch as committed in the run's workspace, which is what the change shows,
// or as published when the workspace has no such branch. Empty for a run
// that is not repository-backed.
func (p landingPins) acceptedTip(ctx context.Context, run RunRef) (string, error) {
	if p.db == nil {
		return "", nil
	}
	source, err := ReadSourceIn(ctx, p.db, run)
	if err != nil || !source.RepoBacked() || source.Branch == "" {
		return "", err
	}
	// A failed read is not a missing branch: an empty answer here would
	// exempt a vote from recording what it accepted. Only a
	// repository that is not there, or one that confirms it has no such
	// branch, moves on: the folder first, then the hub. A branch neither
	// holds is a change with no commit of its own yet, which accepts none.
	ref := "refs/heads/" + source.Branch
	for _, repo := range []string{source.Workspace(p.dataDir), source.Repository(p.dataDir)} {
		if _, err := os.Stat(repo); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("reading %s: %w", repo, err)
		}
		tip, err := gitexec.HubReadRef(ctx, repo, ref)
		if err != nil {
			return "", fmt.Errorf("reading the change's branch: %w", err)
		}
		if tip != "" {
			return tip, nil
		}
	}
	return "", nil
}

func (p landingPins) pinPrepared(ctx context.Context, run *Run) error {
	if p.db == nil {
		return nil
	}
	if _, _, err := LandingCandidateIn(ctx, p.db, run.ID); !errors.Is(err, ErrLandingUnpinned) {
		return err
	}
	source, err := ReadSourceIn(ctx, p.db, RunRef{ID: run.ID, ProjectID: run.ProjectID})
	if err != nil {
		return err
	}
	// A landing publishes nothing: its candidate is the branch as published.
	hub := source.Repository(p.dataDir)
	// A repository with no default branch yet has no target: the candidate
	// lands as the first history. With neither, there is nothing to land and
	// nothing to pin; the landing's gates and merge refuse.
	target := scm.Ref(ctx, hub, "refs/heads/"+source.DefaultBranch)
	candidate := scm.Ref(ctx, hub, "refs/heads/"+source.Branch)
	if candidate == "" {
		candidate = target
	}
	if candidate == "" {
		return nil
	}
	_, err = database.Transact(ctx, p.db, func(tx *database.Tx) (struct{}, error) {
		return struct{}{}, recordReceiptIn(ctx, tx, run.ID, landingCandidateEvent, LandingCandidate{Candidate: candidate, Target: target, By: "admission"}, p.now())
	})
	return err
}

// EndLanding closes the landing whose step this execution finished, for a
// step that found the landing cannot go on as it is (its branch is behind
// main): the landing ends having changed nothing, every other gate with it,
// and its change is back in progress for its worker to fix and land again.
// It is the landing's own root run only, and only after the step's outcome
// is recorded, so the record says why it ended.
func (e *Execution) EndLanding(reason string) error {
	if e.claim == nil || !e.finished {
		return ErrStepAttemptLost
	}
	spec, err := e.run.Definition()
	if err != nil {
		return err
	}
	if !e.run.ParentRunID.IsZero() || spec.Role != "landing" {
		return fmt.Errorf("only a landing's own step ends the landing")
	}
	_, err = e.service.abandonments.abandon(e.ctx, Abandonment{RunID: e.run.ID, Who: "forge-system", Reason: reason}, e.run.ProjectID)
	return err
}

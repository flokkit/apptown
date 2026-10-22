package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/workflow/internal/workflowsql"
	"github.com/flokkit/apptown/internal/database"
)

// approvalEligibility answers whether person could approve step themselves:
// an enabled account, an eligible approver and the approve capability, the
// same checks a manual approval passes. A refusal is a *DecisionDenied; any
// other error is a failure to read policy.
type approvalEligibility func(ctx context.Context, run *Run, step *Step, person access.Actor) error

// mayApprove asks the authority about the accepter without acting as them.
// The decision is recorded like any other, because carrying a yes is an act
// under the accepter's authority.
func (w commandAuthority) mayApprove(ctx context.Context, run *Run, step *Step, who access.Actor) error {
	person := who.Name
	if base, _, ok := strings.Cut(person, " via "); ok {
		person = base
	}
	if person == "" || person == "apptown-system" {
		return &DecisionDenied{Code: access.DenyNotEligible, Reason: "no person accepted this change"}
	}
	// An acceptance AppTown cannot tie to a principal (by an account it
	// could not place) is not carried: its name could be someone else's.
	if who.Principal.IsZero() {
		return &DecisionDenied{Code: access.DenyNotEligible, Reason: fmt.Sprintf("the acceptance names %s but no account AppTown can tie it to; a person approves", person)}
	}
	// The yes is the person's own only if they gave it directly; through a
	// chat, a ticket or an agent it is not their assent to carry.
	if who.Via != "" && !who.Assents() {
		return &DecisionDenied{Code: access.DenyNotEligible, Reason: fmt.Sprintf("the acceptance was given through %s, not by %s directly", who.Way(), person)}
	}
	cfg, err := ReadApprovalConfig(step.Config)
	if err != nil {
		return err
	}
	// Eligibility is the accepting principal's, whatever their name is now.
	pr := access.Principal{ID: who.Principal, Kind: "user", Name: person}
	ctx = access.WithCaller(ctx, access.Caller{Principal: pr, Verified: true, Receipt: person})
	_, err = w.requireDecision(ctx, pr, operationPermission{Action: actionApproveStep, ProjectID: run.ProjectID, ChangeID: run.ChangeID, RunID: run.ID, StepID: step.ID, Approvers: cfg.Approvers})
	return err
}

// LandingMeans supplies what the project's landing approvals would say if
// its landing started now, in order, so an acceptance can show and record
// them.
type LandingMeans interface {
	LandingMeans(ctx context.Context, projectID ids.ID) ([]string, error)
}

// isAcceptance reports whether step is the change gate a landing's approval
// may inherit: an approval in the last stage of a change's working run.
// Earlier approvals, such as a plan's, never are.
func isAcceptance(run *Run, step *Step) bool {
	if run == nil || step == nil || step.Type != "approval" || run.ChangeID.IsZero() || !run.ParentRunID.IsZero() {
		return false
	}
	spec, err := run.Definition()
	if err != nil || spec.Role != "working" || len(spec.Stages) == 0 {
		return false
	}
	return step.Stage == spec.Stages[len(spec.Stages)-1].Name
}

// landingMeansFor answers what the landing would ask if step is an
// acceptance, and nothing otherwise. A landing that cannot be read shows
// nothing, so no acceptance counts for it and its approval waits for a
// person.
func (s *Service) landingMeansFor(ctx context.Context, runID, stepID ids.ID) []string {
	if s.means == nil {
		return nil
	}
	run, err := readRun(ctx, s.db, runID)
	if err != nil {
		return nil
	}
	step, err := ReadStepIn(ctx, s.db, stepID)
	if err != nil || !isAcceptance(run, step) {
		return nil
	}
	means, err := s.means.LandingMeans(ctx, run.ProjectID)
	if err != nil {
		log.Printf("run %d: reading what the landing would ask: %v", runID, err)
		return nil
	}
	return means
}

// judgeCarry decides, per approval step still waiting for the accepter,
// whether the acceptance counts for it: nil where it does, the refusal where
// it does not. It counts only where the acceptance showed the step's own
// sentence, the landing's checks brought no news since, and the accepter
// could approve the step themselves. Without an eligibility check nothing is
// carried.
func (s landings) judgeCarry(ctx context.Context, run *Run, steps, all []*Step, accepted acceptance, news []string) (map[ids.ID]error, error) {
	accepter := accepted.Who
	out := map[ids.ID]error{}
	sourceRefusal, err := carryRefusalIn(ctx, s.db, run.ID, accepted.Source)
	if err != nil {
		return nil, err
	}
	if accepted.InAdvance && sourceRefusal == "" {
		// A yes given in advance names the empty commit it was cast on; it
		// counts only for a landing pinned to that commit, so the landing
		// merges no code.
		if _, pinned, err := LandingCandidateIn(ctx, s.db, run.ID); err != nil {
			return nil, err
		} else if !pinned {
			sourceRefusal = "the landing has no pinned candidate to compare with the commit the yes was given on"
		}
	}
	for _, step := range steps {
		needed, err := completionNeedsVote(step, accepter)
		if err != nil {
			return nil, err
		}
		if !needed || step.Status != StatusPending {
			continue
		}
		cfg, err := ReadApprovalConfig(step.Config)
		if err != nil {
			return nil, err
		}
		means := cfg.Means
		if accepted.InAdvance {
			// A yes given in advance counts only for an approval that says
			// what such a yes means, and the person was shown that.
			means = cfg.InAdvance
		}
		if means == "" || !slices.Contains(accepted.Shown, means) {
			// A yes counts only for a sentence the person read when they gave it.
			out[step.ID] = &DecisionDenied{Code: access.DenyNotEligible, Reason: fmt.Sprintf("the acceptance did not show what this approval means (“%s”)", means)}
			continue
		}
		if sourceRefusal != "" {
			// The yes was given for the work as accepted; a person approves
			// the commit this landing will merge.
			out[step.ID] = &DecisionDenied{Code: access.DenyNotEligible, Reason: sourceRefusal}
			continue
		}
		if refusal := s.addsRefusal(ctx, run, step, accepted); refusal != "" {
			// The yes was given to a list of what the change adds; it counts
			// only where the landing's list is the same.
			out[step.ID] = &DecisionDenied{Code: access.DenyNotEligible, Reason: refusal}
			continue
		}
		if accepted.InAdvance {
			// Given before any of it existed, the yes holds only where every
			// gate passed outright; news alone is not the bar.
			if why := passedOutright(all); why != "" {
				out[step.ID] = &DecisionDenied{Code: access.DenyNotEligible, Reason: "the yes was given in advance, on condition every gate passes outright: " + why}
				continue
			}
		}
		if len(news) > 0 {
			// The yes was given before these existed; the person looks again.
			out[step.ID] = &DecisionDenied{Code: access.DenyNotEligible, Reason: "the landing's checks raised something to look at since the acceptance: " + strings.Join(news, "; ")}
			continue
		}
		if s.eligible == nil {
			out[step.ID] = &DecisionDenied{Code: access.DenyNotEligible, Reason: "approval eligibility is unavailable"}
			continue
		}
		err = s.eligible(ctx, run, step, accepter)
		var denied *DecisionDenied
		switch {
		case err == nil, errors.As(err, &denied):
			out[step.ID] = err
		default:
			return nil, err
		}
	}
	return out, nil
}

// checkNews is what a landing's checks said that the accepter could not have
// known when they accepted: an override, a step that did not pass, checks
// that errored, or a check that could not judge the change. What is true of
// every landing is not news: a scan point with no scanner attached, a runtime
// check AppTown itself could not run (stock land starts no preview), a
// model check with no model bound. Nor is what already fails on main: a
// finding on main too is the estate's, not this change's doing, whether the
// gate could split its occurrences (preexisting) or not (undecided, which
// the change's reviewer already judged from its diff). Holding the carried
// yes for it would hold every landing of a project with old debt. What the
// change introduced fails the gate, which is news.
func checkNews(steps []*Step) []string {
	var news []string
	for _, step := range steps {
		if step.Type == "approval" {
			continue
		}
		if step.OverrideBy != "" {
			news = append(news, fmt.Sprintf("%s was overridden by %s", step.Name, step.OverrideBy))
			continue
		}
		switch step.Status {
		case StatusFail, StatusError, StatusNeedsHuman:
			news = append(news, fmt.Sprintf("%s did not pass", step.Name))
			continue
		}
		if !isCheckKind(step.Type) {
			continue
		}
		evidence, err := ReadCheckEvidence(step.Evidence)
		if err != nil {
			news = append(news, fmt.Sprintf("%s's check evidence could not be read", step.Name))
			continue
		}
		if evidence.CheckErrors > 0 {
			news = append(news, fmt.Sprintf("%s had %d check error(s)", step.Name, evidence.CheckErrors))
		}
		for _, c := range evidence.Checks {
			if c.Verdict == VerdictCannotAssess && !c.AppTownFault {
				news = append(news, fmt.Sprintf("%s could not judge %s", step.Name, c.Check))
			}
		}
	}
	return news
}

// settled reports whether every step of the stage other than its approvals
// has answered, so the acceptance is judged against the stage's results.
func settled(steps []*Step, stage string) bool {
	for _, step := range steps {
		if step.Stage == stage && step.Type != "approval" && (step.Status == StatusPending || step.Status == StatusRunning) {
			return false
		}
	}
	return true
}

type acceptanceCarried struct {
	StepID   ids.ID `json:"stepId"`
	Step     string `json:"step"`
	Accepter string `json:"accepter"`
	WorkRun  ids.ID `json:"workRun"`
}
type acceptanceWithheld struct {
	StepID   ids.ID `json:"stepId"`
	Step     string `json:"step"`
	Accepter string `json:"accepter"`
	WorkRun  ids.ID `json:"workRun"`
	Reason   string `json:"reason"`
}

// carryVoteIn casts the accepter's vote on one landing approval, or records
// why it was withheld so the waiting step explains itself. Either receipt
// marks the step as judged. It reports whether a vote was cast.
func (s landings) carryVoteIn(ctx context.Context, tx *database.Tx, landing *Run, step *Step, who access.Actor, adds *ChangeAdds, workID ids.ID, refused error, inAdvance string) (bool, error) {
	accepter := who.Name
	if refused != nil {
		return false, recordReceiptIn(ctx, tx, landing.ID, "landing.acceptance.withheld", acceptanceWithheld{step.ID, step.Name, accepter, workID, refused.Error()}, s.now())
	}
	// The carried vote is the accepter's: their principal and the way they
	// accepted, so independence and assent judge the person, not the name.
	if _, err := (reworkStore{s.db, s.clocked}).approveIn(ctx, tx, landing, step, ApprovalVote{Approver: accepter, Principal: who.Principal, Via: who.Via, Decision: VoteApprove, Comment: fmt.Sprintf("accepted at the change gate (run %d); landing rolls on — gates still enforce", workID), Adds: adds, InAdvance: inAdvance}); err != nil {
		return false, err
	}
	return true, recordReceiptIn(ctx, tx, landing.ID, "landing.acceptance.carried", acceptanceCarried{step.ID, step.Name, accepter, workID}, s.now())
}

// carry is the one place an accepted change's acceptance is applied to its
// landing. For the approvals waiting in the landing's current stage, once
// every other step of that stage has answered, it judges each approval once
// (judgeCarry) and casts the accepter's vote or records why not. It reports
// whether a vote was cast, so the caller can advance a stage that needs
// nothing else.
//
// The engine calls it whenever it advances a run and when a stage opens with
// nothing automatic to run; a run that is not an accepted landing costs one
// definition read. A failure leaves the approval waiting for a person, which
// is the safe direction, and a later advance tries again.
func (s landings) carry(ctx context.Context, run *Run) (bool, error) {
	if run == nil || run.Status != RunRunning || run.ChangeID.IsZero() || !run.ParentRunID.IsZero() {
		return false, nil
	}
	spec, err := run.Definition()
	if err != nil || spec.Role != "landing" {
		return false, err
	}
	q := workflowsql.New(s.db.SQL())
	workID, err := q.LandingAcceptedWork(ctx, run.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	steps, err := readSteps(ctx, s.db, run.ID)
	if err != nil {
		return false, err
	}
	considered, err := q.LandingAcceptanceConsidered(ctx, run.ID)
	if err != nil {
		return false, err
	}
	var waiting []*Step
	for _, step := range steps {
		if step.Stage == run.CurrentStage && step.Type == "approval" && step.Status == StatusPending && !slices.Contains(considered, step.ID) {
			waiting = append(waiting, step)
		}
	}
	if len(waiting) == 0 || !settled(steps, run.CurrentStage) {
		return false, nil
	}
	accepted, err := database.Transact(ctx, s.db, func(tx *database.Tx) (acceptance, error) {
		return acceptanceIn(ctx, tx, workID)
	})
	if err != nil {
		return false, err
	}
	// Judge before the vote's transaction: eligibility reads policy on other
	// connections, which must not wait behind this one's locks.
	decisions, err := s.judgeCarry(ctx, run, waiting, steps, accepted, checkNews(steps))
	if err != nil || len(decisions) == 0 {
		return false, err
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (bool, error) {
		current, err := lockRunSnapshotIn(ctx, tx, run)
		if err != nil {
			return false, err
		}
		again, err := workflowsql.New(tx.SQL()).LandingAcceptanceConsidered(ctx, current.ID)
		if err != nil {
			return false, err
		}
		locked, err := stepRecords(workflowsql.New(tx.SQL()).StageStepsForUpdate(ctx, workflowsql.StageStepsForUpdateParams{RunID: current.ID, Stage: current.CurrentStage}))
		if err != nil {
			return false, err
		}
		// The run's facts are judged again under the locks every step decision
		// takes: an override or a check that settled since the judgement above
		// is seen here, not missed. Eligibility
		// is not re-asked here: like a manual approval, it is checked just
		// before the write's transaction, never inside it.
		all, err := readSteps(ctx, tx, current.ID)
		if err != nil {
			return false, err
		}
		if !settled(all, current.CurrentStage) {
			return false, nil
		}
		if news := checkNews(all); len(news) > 0 {
			for id, refused := range decisions {
				if refused == nil {
					decisions[id] = &DecisionDenied{Code: access.DenyNotEligible, Reason: "the landing's checks raised something to look at since the acceptance: " + strings.Join(news, "; ")}
				}
			}
		}
		if why := passedOutright(all); accepted.InAdvance && why != "" {
			for id, refused := range decisions {
				if refused == nil {
					decisions[id] = &DecisionDenied{Code: access.DenyNotEligible, Reason: "the yes was given in advance, on condition every gate passes outright: " + why}
				}
			}
		}
		cast := false
		for _, step := range locked {
			refused, judged := decisions[step.ID]
			if !judged || slices.Contains(again, step.ID) {
				continue
			}
			if needed, err := completionNeedsVote(step, accepted.Who); err != nil || !needed {
				if err != nil {
					return false, err
				}
				continue
			}
			// A yes given in advance is carried as one, with the sentence
			// this approval gives it.
			inAdvance := ""
			if accepted.InAdvance {
				if cfg, err := ReadApprovalConfig(step.Config); err == nil {
					inAdvance = cfg.InAdvance
				}
			}
			voted, err := s.carryVoteIn(ctx, tx, current, step, accepted.Who, accepted.Adds, workID, refused, inAdvance)
			if err != nil {
				return false, err
			}
			cast = cast || voted
		}
		return cast, nil
	})
}

// ApprovalQuestion is what a person agrees to by approving a step: its own
// sentence and, for a change's waiting acceptance, what the landing's
// approvals would ask, for which the acceptance may count.
type ApprovalQuestion struct {
	Step         string   `json:"step"`
	Status       string   `json:"status"`
	Means        string   `json:"means"`
	Instructions string   `json:"instructions"`
	LandingMeans []string `json:"landingMeans"`
	// Adds is what the approval's commit brings into the app, for an
	// approval waiting on a decision that assents to a commit. An approving
	// vote sends its digest back.
	Adds *ChangeAdds `json:"adds,omitempty"`
}

// ApprovalQuestion reads what approving step id would agree to, for clients
// that show it before sending the decision, as the web's decision form does.
func (s *Service) ApprovalQuestion(ctx context.Context, id ids.ID) (ApprovalQuestion, error) {
	read, err := s.Step(ctx, id)
	if err != nil {
		return ApprovalQuestion{}, err
	}
	step := read.Step
	if step.Type != "approval" {
		return ApprovalQuestion{}, fmt.Errorf("%w: step is not an approval", errStepApprovalInput)
	}
	var cfg ApprovalConfig
	if err := approvalObject(step.Config, &cfg); err != nil {
		return ApprovalQuestion{}, err
	}
	out := ApprovalQuestion{Step: step.Name, Status: step.Status.String(), Means: cfg.Means, Instructions: cfg.Instructions, LandingMeans: []string{}}
	if step.Status == StatusPending && isAcceptance(read.Run, step) {
		if means := s.landingMeansFor(ctx, read.Run.ID, step.ID); means != nil {
			out.LandingMeans = means
		}
	}
	if step.Status == StatusPending {
		if out.Adds, err = s.adds.forStep(ctx, read.Run, step); err != nil {
			return ApprovalQuestion{}, err
		}
	}
	return out, nil
}

// addsRefusal is why an acceptance's yes does not count for a landing
// approval on what the change adds, or "" when it does: the landing's list
// for its candidate must be the list the accepter was shown. A list that
// cannot be read refuses; the approval waits for a person.
func (s landings) addsRefusal(ctx context.Context, run *Run, step *Step, accepted acceptance) string {
	landing, err := s.adds.forStep(ctx, run, step)
	switch {
	case err != nil:
		return "what the landing's candidate adds could not be read: " + err.Error()
	case landing == nil:
		return ""
	case accepted.Adds == nil:
		return "the acceptance did not record what the change adds"
	case !accepted.Adds.sameRows(*landing):
		return "what this change adds has changed since it was accepted"
	}
	return ""
}

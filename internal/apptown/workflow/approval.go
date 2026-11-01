package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// ApprovalDecision is one person's immutable vote, not a step lifecycle state.
// A round may remain pending after an approval; any rejection closes the round.
type ApprovalDecision uint8

const (
	VoteApprove ApprovalDecision = iota + 1
	VoteReject
)

var errApprovalDecision = fault.New(fault.Invalid, "decision must be approve or reject")

func (d ApprovalDecision) String() string {
	switch d {
	case VoteApprove:
		return "approve"
	case VoteReject:
		return "reject"
	default:
		return "invalid-approval-decision"
	}
}
func ParseApprovalDecision(text string) (ApprovalDecision, error) {
	switch text {
	case "approve":
		return VoteApprove, nil
	case "reject":
		return VoteReject, nil
	default:
		return 0, errApprovalDecision
	}
}
func (d ApprovalDecision) MarshalJSON() ([]byte, error) {
	if d != VoteApprove && d != VoteReject {
		return nil, errApprovalDecision
	}
	return json.Marshal(d.String())
}
func (d *ApprovalDecision) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	value, err := ParseApprovalDecision(text)
	if err != nil {
		return err
	}
	*d = value
	return nil
}

// ApprovalVote is one person's decision on the current round.
type ApprovalVote struct {
	original json.RawMessage
	Approver string `json:"approver"`
	// Principal is the person who voted and Via how (access.Actor); a vote
	// AppTown could not tie to a person has neither and counts for no one.
	Principal ids.ID           `json:"principal,omitzero"`
	Via       string           `json:"via,omitempty"`
	Decision  ApprovalDecision `json:"decision"`
	Comment   string           `json:"comment"`
	At        string           `json:"at"`
	// AlsoApproves is what the project's landing approvals said when this
	// vote accepted a change, as shown beside the acceptance. The acceptance
	// counts for a landing approval only if its sentence is here.
	AlsoApproves []string `json:"alsoApproves,omitempty"`
	// Candidate is the commit this vote assents to: a checked landing's
	// candidate, or, for an acceptance, the branch as published when it was
	// given. A vote is assent to that commit, not to whatever lands.
	Candidate string `json:"candidate,omitempty"`
	// Adds is what the commit brought into the app as this vote was shown
	// it (ApprovalQuestion), or, for a vote carried from an acceptance, as
	// the accepter was shown it. Absent on rejections and on votes recorded
	// before the list existed.
	Adds *ChangeAdds `json:"adds,omitempty"`
	// InAdvance is what this yes meant when it was given before the work
	// existed: the step's declared inAdvance sentence, which the person was
	// shown. Empty for a yes given to the work itself, which means the
	// round's sentence (ApprovalEvidence.Means).
	InAdvance string `json:"inAdvance,omitempty"`
}

// ApprovalRejection is the feedback retained when a rejected round reopens.
// Its kind is implicit; it cannot accidentally carry an approving vote.
type ApprovalRejection struct {
	original json.RawMessage
	Approver string `json:"approver"`
	Comment  string `json:"comment"`
	At       string `json:"at"`
}

// Withdrawal closes acceptance because its owning work item was withdrawn.
// It is not a quorum vote, and does not erase anyone's recorded decisions.
type ApprovalWithdrawal struct {
	By     string `json:"by"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

func (w ApprovalWithdrawal) validate() error {
	if strings.TrimSpace(w.By) == "" || strings.TrimSpace(w.Reason) == "" || w.At == "" {
		return fmt.Errorf("approval withdrawal requires its actor, reason and time")
	}
	return nil
}

// ApprovalEvidence records current and previous rounds. The top-level decision
// identifies the closing vote. LastRejection remains available to feedback while
// fresh votes are collected; it is not a vote in the new round.
type ApprovalEvidence struct {
	Withdrawal          *ApprovalWithdrawal  `json:"withdrawal,omitempty"`
	PreviousWithdrawals []ApprovalWithdrawal `json:"previousWithdrawals,omitempty"`
	PreviousDecisions   []ApprovalVote       `json:"previousDecisions,omitempty"`
	Decisions           []ApprovalVote       `json:"decisions"`
	Quorum              int                  `json:"quorum"`
	Approvals           int                  `json:"approvals"`
	Means               string               `json:"means,omitempty"`
	Approver            string               `json:"approver,omitempty"`
	Decision            ApprovalDecision     `json:"decision,omitempty"`
	Comment             string               `json:"comment,omitempty"`
	At                  string               `json:"at,omitempty"`
	LastRejection       *ApprovalRejection   `json:"lastRejection,omitempty"`
	// Candidate is the landing candidate the deciding round approved.
	Candidate string `json:"candidate,omitempty"`
	// EvalAuthorised names the eval whose start settled this approval in one
	// of its copies (eval_authority.go): no person approved it, and it is
	// never acceptance.
	EvalAuthorised string `json:"evalAuthorised,omitempty"`
}

// StandingApproval names the bounded alternative an approval purpose permits.
// Declaring it supplies no consent and never casts a human vote.
type StandingApproval struct {
	Class string `json:"class"`
	Means string `json:"means"`
}

type ApprovalConfig struct {
	Standing  *StandingApproval `json:"standing,omitempty"`
	Quorum    int               `json:"quorum"`
	Means     string            `json:"means"`
	Approvers []string          `json:"approvers"`
	// InAdvance is what a yes given before the work exists means, on the
	// condition AppTown witnesses (in_advance.go). A step without it takes
	// no yes in advance.
	InAdvance string `json:"inAdvance,omitempty"`
	// InAdvanceAdmitsNotApplicable says the InAdvance sentence counts a
	// check that is not applicable, or a step that is, as meeting its
	// condition. Without it the condition is every check passing, as the
	// sentences written before not applicable existed say.
	InAdvanceAdmitsNotApplicable bool `json:"inAdvanceAdmitsNotApplicable,omitempty"`
	// Instructions is what the person deciding is told to look at, shown
	// with the meaning (ApprovalQuestion).
	Instructions string `json:"instructions,omitempty"`
}

func approvalObject(raw []byte, out any) error {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	if data[0] != '{' {
		return fmt.Errorf("approval data must be an object")
	}
	return json.Unmarshal(data, out)
}
func ReadApprovalConfig(raw []byte) (ApprovalConfig, error) {
	var cfg ApprovalConfig
	if err := approvalObject(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid approval configuration: %w", err)
	}
	return cfg.normalized()
}
func (cfg ApprovalConfig) normalized() (ApprovalConfig, error) {
	if cfg.Quorum < 0 {
		return cfg, fmt.Errorf("approval quorum cannot be negative")
	}
	if cfg.Quorum == 0 {
		cfg.Quorum = 1
	}
	if cfg.Standing != nil {
		if cfg.Standing.Class != "routine-update" || strings.TrimSpace(cfg.Standing.Means) == "" {
			return cfg, fmt.Errorf("standing approval requires the routine-update class and its meaning")
		}
		if cfg.Quorum != 1 {
			return cfg, fmt.Errorf("standing approval does not replace a multiple-person quorum")
		}
	}
	return cfg, nil
}
func ReadApprovalEvidence(raw []byte) (ApprovalEvidence, error) {
	var evidence ApprovalEvidence
	if err := approvalObject(raw, &evidence); err != nil {
		return evidence, fmt.Errorf("invalid approval evidence: %w", err)
	}
	return evidence, evidence.validate()
}
func (e ApprovalEvidence) validate() error {
	if e.Withdrawal != nil {
		if err := e.Withdrawal.validate(); err != nil {
			return err
		}
		if e.Decision != 0 {
			return fmt.Errorf("withdrawn approval cannot also have a closing vote")
		}
	}
	for _, withdrawal := range e.PreviousWithdrawals {
		if err := withdrawal.validate(); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(e.Decisions))
	for _, vote := range e.Decisions {
		if strings.TrimSpace(vote.Approver) == "" || (vote.Decision != VoteApprove && vote.Decision != VoteReject) {
			return fmt.Errorf("invalid saved approval vote")
		}
		// One vote per person: by principal where the vote names one (two
		// people may have voted under one name either side of a rename), by
		// name where it does not.
		key := "name:" + vote.Approver
		if !vote.Principal.IsZero() {
			key = "principal:" + vote.Principal.String()
		}
		if seen[key] {
			return fmt.Errorf("duplicate saved approval vote by %s", vote.Approver)
		}
		seen[key] = true
	}
	for _, vote := range e.PreviousDecisions {
		if strings.TrimSpace(vote.Approver) == "" || (vote.Decision != VoteApprove && vote.Decision != VoteReject) {
			return fmt.Errorf("invalid previous approval vote")
		}
	}
	return nil
}

// HasVoted is whether the person has voted in this round.
func (e ApprovalEvidence) HasVoted(person ids.ID) bool {
	return e.hasVoted(ApprovalVote{Principal: person})
}

// hasVoted is whether the vote's person has voted in this round: the same
// principal, whatever name either vote was written with. A vote AppTown
// could not tie to a person matches no one.
func (e ApprovalEvidence) hasVoted(vote ApprovalVote) bool {
	if vote.Principal.IsZero() {
		return false
	}
	for _, earlier := range e.Decisions {
		if earlier.Principal == vote.Principal {
			return true
		}
	}
	return false
}

// people is how many distinct people have approved in this round, by
// principal. A vote whose name AppTown could not tie to a person (a chat's
// label, an account it could not place) may be anyone's, so it does not
// count as a person.
func (e ApprovalEvidence) people() int {
	principals := map[ids.ID]bool{}
	for _, vote := range e.Decisions {
		if vote.Decision == VoteApprove && !vote.Principal.IsZero() {
			principals[vote.Principal] = true
		}
	}
	return len(principals)
}

func (e ApprovalEvidence) clone() ApprovalEvidence {
	e.PreviousWithdrawals = slices.Clone(e.PreviousWithdrawals)
	if e.Withdrawal != nil {
		copy := *e.Withdrawal
		e.Withdrawal = &copy
	}
	e.Decisions = slices.Clone(e.Decisions)
	e.PreviousDecisions = slices.Clone(e.PreviousDecisions)
	for i := range e.Decisions {
		e.Decisions[i].original = bytes.Clone(e.Decisions[i].original)
	}
	for i := range e.PreviousDecisions {
		e.PreviousDecisions[i].original = bytes.Clone(e.PreviousDecisions[i].original)
	}
	if e.LastRejection != nil {
		r := *e.LastRejection
		r.original = bytes.Clone(r.original)
		e.LastRejection = &r
	}
	return e
}

// cast is the one rule for a vote's effect. Counts come from distinct saved
// votes, never a caller's cached count. Returned evidence shares no mutable round
// state with the input. Storage commits it with the vote receipt and any rework.
func (e ApprovalEvidence) cast(status StepState, cfg ApprovalConfig, vote ApprovalVote) (ApprovalEvidence, StepState, bool, error) {
	if vote.Decision != VoteApprove && vote.Decision != VoteReject {
		return ApprovalEvidence{}, 0, false, errApprovalDecision
	}
	if strings.TrimSpace(vote.Approver) == "" {
		return ApprovalEvidence{}, 0, false, errStepDecisionCaller
	}
	if _, err := status.transition(moveStepVote, status); err != nil {
		return ApprovalEvidence{}, 0, false, fmt.Errorf("this approval is already decided: %w: %w", errStepDecisionConflict, err)
	}
	previous := status
	cfg, _, err := e.openRound(cfg)
	if err != nil {
		return ApprovalEvidence{}, 0, false, err
	}
	if e.hasVoted(vote) {
		return ApprovalEvidence{}, 0, false, fmt.Errorf("%s has already decided on this step: %w", vote.Approver, errStepDecisionConflict)
	}
	for _, earlier := range e.Decisions {
		// Approvals that form a quorum must all be for one commit. A
		// rejection closes the round whatever the earlier votes named.
		if vote.Decision == VoteApprove && earlier.Candidate != vote.Candidate {
			return ApprovalEvidence{}, 0, false, fmt.Errorf("earlier votes on this approval were for another landing candidate; it must be asked again: %w", errStepDecisionConflict)
		}
	}
	next := e.clone()
	vote.original = bytes.Clone(vote.original)
	next.Decisions = append(next.Decisions, vote)
	next.Quorum, next.Approvals, next.Means = cfg.Quorum, next.people(), cfg.Means
	next.Approver, next.Decision, next.Comment, next.At, next.Candidate = "", 0, "", "", ""
	pending := vote.Decision == VoteApprove && next.Approvals < next.Quorum
	if !pending {
		next.Approver, next.Decision, next.Comment, next.At, next.Candidate = vote.Approver, vote.Decision, vote.Comment, vote.At, vote.Candidate
		status = StatusFail
		if vote.Decision == VoteApprove {
			status = StatusPass
		}
	}
	if _, err := previous.transition(moveStepVote, status); err != nil {
		return ApprovalEvidence{}, 0, false, err
	}
	return next, status, pending, nil
}

// reopen retains the rejected round as history and feedback, with no votes
// counting toward the next round. Only a failed approval can be reopened.
func (e ApprovalEvidence) reopen(status StepState) (ApprovalEvidence, error) {
	if _, err := status.transition(moveStepReopen, StatusPending); err != nil {
		return ApprovalEvidence{}, fmt.Errorf("only a rejected approval can reopen: %w", err)
	}
	if err := e.validate(); err != nil {
		return ApprovalEvidence{}, err
	}
	next := e.clone()
	if e.Withdrawal != nil {
		next.PreviousWithdrawals = append(next.PreviousWithdrawals, *e.Withdrawal)
		next.Withdrawal = nil
	} else {
		next.LastRejection = &ApprovalRejection{Approver: e.Approver, Comment: e.Comment, At: e.At}
	}
	next.PreviousDecisions = append(next.PreviousDecisions, next.Decisions...)
	next.Decisions = nil
	next.Approvals = 0
	next.Approver, next.Decision, next.Comment, next.At = "", 0, "", ""
	return next, nil
}

func (e ApprovalEvidence) withdraw(status StepState, cfg ApprovalConfig, withdrawal ApprovalWithdrawal) (ApprovalEvidence, StepState, error) {
	nextState, err := status.transition(moveStepWithdraw, StatusFail)
	if err != nil {
		return ApprovalEvidence{}, 0, err
	}
	if err := withdrawal.validate(); err != nil {
		return ApprovalEvidence{}, 0, err
	}
	cfg, approvals, err := e.openRound(cfg)
	if err != nil {
		return ApprovalEvidence{}, 0, err
	}
	next := e.clone()
	next.Withdrawal = &withdrawal
	next.Quorum, next.Approvals, next.Means = cfg.Quorum, approvals, cfg.Means
	next.Approver, next.Decision, next.Comment, next.At = "", 0, "", ""
	return next, nextState, nil
}

// Current votes and the step lifecycle define an open round. The top-level
// closing vote is a display summary, not a second source of lifecycle authority.
func (e ApprovalEvidence) openRound(cfg ApprovalConfig) (ApprovalConfig, int, error) {
	if err := e.validate(); err != nil {
		return cfg, 0, err
	}
	if e.Withdrawal != nil {
		return cfg, 0, fmt.Errorf("this acceptance was withdrawn; reopen it before deciding: %w", errStepDecisionConflict)
	}
	var err error
	cfg, err = cfg.normalized()
	if err != nil {
		return cfg, 0, err
	}
	for _, saved := range e.Decisions {
		if saved.Decision == VoteReject {
			return cfg, 0, fmt.Errorf("this approval round was rejected; reopen it before deciding: %w", errStepDecisionConflict)
		}
	}
	approvals := e.people()
	if approvals >= cfg.Quorum {
		return cfg, 0, fmt.Errorf("this approval already has its quorum: %w", errStepDecisionConflict)
	}
	return cfg, approvals, nil
}

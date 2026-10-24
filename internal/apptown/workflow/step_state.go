package workflow

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/enum"
)

// StepState describes a check or execution attempt, not its parent run. A retry
// may reopen a step; only its owned attempt may publish an execution outcome.
type StepState uint8

const (
	StatusPending StepState = iota + 1
	StatusRunning
	StatusPass
	StatusFail
	StatusNeedsHuman
	StatusError
	StatusSkipped
	// StatusNotApplicable: the step ran and found its subject is not part
	// of this app (the contract declares nothing it applies to). It lets a
	// gate through and is never a pass. Not StatusSkipped, the dispatcher's
	// word for a step whose condition said not to run it.
	StatusNotApplicable
)

// stepStateCodec spells each StepState in storage and JSON.
var stepStateCodec = enum.Set[StepState]{
	Names:     []string{"pending", "running", "pass", "fail", "needs_human", "error", "skipped", "not_applicable"},
	Invalid:   "invalid-step-state",
	Err:       errStepState,
	ScanLabel: "database value",
}

var (
	errStepState      = errors.New("invalid step state")
	errStepTransition = errors.New("invalid step transition")
)

type StepMove uint8

const (
	MoveStepStart StepMove = iota + 1
	MoveStepFinish
	MoveStepVote
	MoveStepReopen
	MoveStepOverride
	MoveStepReset
	MoveStepEvaluate
	MoveStepWithdraw
	MoveStepSkip
)

// Transition is the single state rule for each kind of step operation. Stores
// additionally enforce scope, stage, attempt/revision and operation-specific
// evidence under their existing locks, then commit the state and receipt together.
func (s StepState) Transition(move StepMove, next StepState) (StepState, error) {
	if _, err := s.Value(); err != nil {
		return 0, err
	}
	if _, err := next.Value(); err != nil {
		return 0, err
	}
	allowed := false
	switch move {
	case MoveStepStart:
		allowed = s == StatusPending && next == StatusRunning
	case MoveStepFinish:
		allowed = s == StatusRunning && (next == StatusPass || next == StatusFail || next == StatusNeedsHuman || next == StatusError || next == StatusSkipped || next == StatusNotApplicable)
	case MoveStepVote:
		allowed = (s == StatusPending || s == StatusNeedsHuman) && (next == s || next == StatusPass || next == StatusFail)
	case MoveStepReopen:
		allowed = s == StatusFail && next == StatusPending
	case MoveStepOverride:
		allowed = s != StatusRunning && s != StatusPass && s != StatusNotApplicable && next == StatusPass
	case MoveStepReset:
		allowed = s != StatusRunning && next == StatusPending
	case MoveStepEvaluate:
		allowed = s != StatusRunning && (next == StatusPass || next == StatusFail || next == StatusNeedsHuman || next == StatusError)
	case MoveStepWithdraw:
		allowed = (s == StatusPending || s == StatusNeedsHuman) && next == StatusFail
	case MoveStepSkip:
		allowed = (s == StatusPending || s == StatusError) && next == StatusSkipped
	}
	if !allowed {
		return 0, fmt.Errorf("%w: operation %d cannot move %s to %s", errStepTransition, move, s, next)
	}
	return next, nil
}

func (s StepState) PassesGate() bool {
	return s == StatusPass || s == StatusSkipped || s == StatusNotApplicable
}

// Judged says the step judged its subject and let it through: a pass, or
// not applicable. Neither a skip nor an override is a judgement.
func (s StepState) Judged() bool                     { return s == StatusPass || s == StatusNotApplicable }
func (s StepState) String() string                   { return stepStateCodec.String(s) }
func parseStepState(text string) (StepState, error)  { return stepStateCodec.Parse(text) }
func (s StepState) Value() (driver.Value, error)     { return stepStateCodec.Value(s) }
func (s *StepState) Scan(value any) error            { return stepStateCodec.Scan(s, value) }
func (s StepState) MarshalJSON() ([]byte, error)     { return stepStateCodec.JSON(s) }
func (s *StepState) UnmarshalJSON(data []byte) error { return stepStateCodec.ReadJSON(s, data) }

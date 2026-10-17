// Package adrs owns architecture decision records and their lifecycle.
package adrs

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"flokkit.io/forge/internal/fault"
)

type State uint8

const (
	Draft State = iota + 1
	Proposed
	Accepted
	Rejected
	Deprecated
	Superseded
)

var ErrState = fault.New(fault.Unprocessable, "invalid ADR state")
var ErrTransition = fault.New(fault.Unprocessable, "cannot transition ADR")
var ErrImmutable = fault.New(fault.Conflict, "accepted ADR content is immutable; propose a successor instead")
var ErrConflict = fault.New(fault.Conflict, "ADR changed or its required write was not recorded")
var ErrInput = fault.New(fault.Invalid, "invalid ADR input")

// ErrTextExpected refuses a text edit that does not say what text it started
// from; ErrTextMoved refuses one whose starting text is no longer stored, so
// two people editing one decision cannot silently replace each other's text.
var ErrTextExpected = fault.New(fault.Invalid, "send the text this edit started from (expected) for each field it changes")
var ErrTextMoved error = textMoved{}

type textMoved struct{}

func (textMoved) Error() string {
	return "this decision's text was edited since this edit started; review the current text before saving again"
}
func (textMoved) FaultKind() fault.Kind { return fault.Conflict }
func (textMoved) FaultCode() string     { return "adr-text-moved" }

func (s State) String() string {
	switch s {
	case Draft:
		return "draft"
	case Proposed:
		return "proposed"
	case Accepted:
		return "accepted"
	case Rejected:
		return "rejected"
	case Deprecated:
		return "deprecated"
	case Superseded:
		return "superseded"
	}
	return "invalid-adr-state"
}
func (s State) Locked() bool { return s == Accepted || s == Deprecated || s == Superseded }
func ParseState(text string) (State, error) {
	for _, s := range []State{Draft, Proposed, Accepted, Rejected, Deprecated, Superseded} {
		if s.String() == text {
			return s, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrState, text)
}
func (s State) Value() (driver.Value, error) {
	if s < Draft || s > Superseded {
		return nil, ErrState
	}
	return s.String(), nil
}
func (s *State) Scan(v any) error {
	var text string
	switch v := v.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return ErrState
	}
	next, err := ParseState(text)
	if err == nil {
		*s = next
	}
	return err
}
func (s State) MarshalJSON() ([]byte, error) {
	v, err := s.Value()
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
func (s *State) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	return s.Scan(text)
}

// Decision names the requested business act. Retirement is private: only
// accepting a successor may supersede an existing accepted decision.
type Decision uint8

const (
	Redraft Decision = iota + 1
	Propose
	Accept
	Reject
	Deprecate
	retire
)

func ParseDecision(text string) (Decision, error) {
	s, err := ParseState(text)
	if err != nil {
		return 0, err
	}
	switch s {
	case Draft:
		return Redraft, nil
	case Proposed:
		return Propose, nil
	case Accepted:
		return Accept, nil
	case Rejected:
		return Reject, nil
	case Deprecated:
		return Deprecate, nil
	}
	return 0, fmt.Errorf("%w: supersession requires an accepted successor", ErrTransition)
}
func (s State) decide(act Decision) (State, error) {
	if _, err := s.Value(); err != nil {
		return 0, err
	}
	next := State(0)
	switch act {
	case Redraft:
		next = Draft
	case Propose:
		next = Proposed
	case Accept:
		next = Accepted
	case Reject:
		next = Rejected
	case Deprecate:
		next = Deprecated
	case retire:
		next = Superseded
	}
	if next == s {
		return s, nil
	}
	allowed := false
	switch s {
	case Draft:
		allowed = next == Proposed || next == Accepted || next == Rejected
	case Proposed:
		allowed = next == Draft || next == Accepted || next == Rejected
	case Accepted:
		allowed = next == Deprecated || next == Superseded
	case Rejected:
		allowed = next == Draft
	case Deprecated:
		allowed = next == Accepted
	}
	if !allowed {
		return 0, fmt.Errorf("%w: %s cannot act %d", ErrTransition, s, act)
	}
	return next, nil
}

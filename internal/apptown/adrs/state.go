// Package adrs owns architecture decision records and their lifecycle.
package adrs

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/fault"
)

type State uint8

const (
	Draft State = iota + 1
	proposed
	Accepted
	rejected
	Deprecated
	Superseded
)

var errState = fault.New(fault.Unprocessable, "invalid ADR state")
var errTransition = fault.New(fault.Unprocessable, "cannot transition ADR")
var errImmutable = fault.New(fault.Conflict, "accepted ADR content is immutable; propose a successor instead")
var errConflict = fault.New(fault.Conflict, "ADR changed or its required write was not recorded")
var errInput = fault.New(fault.Invalid, "invalid ADR input")

// errTextExpected refuses a text edit that does not say what text it started
// from; ErrTextMoved refuses one whose starting text is no longer stored, so
// two people editing one decision cannot silently replace each other's text.
var errTextExpected = fault.New(fault.Invalid, "send the text this edit started from (expected) for each field it changes")
var errTextMoved error = textMoved{}

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
	case proposed:
		return "proposed"
	case Accepted:
		return "accepted"
	case rejected:
		return "rejected"
	case Deprecated:
		return "deprecated"
	case Superseded:
		return "superseded"
	}
	return "invalid-adr-state"
}
func (s State) locked() bool { return s == Accepted || s == Deprecated || s == Superseded }
func ParseState(text string) (State, error) {
	for _, s := range []State{Draft, proposed, Accepted, rejected, Deprecated, Superseded} {
		if s.String() == text {
			return s, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", errState, text)
}
func (s State) Value() (driver.Value, error) {
	if s < Draft || s > Superseded {
		return nil, errState
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
		return errState
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
	redraft Decision = iota + 1
	Propose
	Accept
	reject
	deprecate
	retire
)

func ParseDecision(text string) (Decision, error) {
	s, err := ParseState(text)
	if err != nil {
		return 0, err
	}
	switch s {
	case Draft:
		return redraft, nil
	case proposed:
		return Propose, nil
	case Accepted:
		return Accept, nil
	case rejected:
		return reject, nil
	case Deprecated:
		return deprecate, nil
	}
	return 0, fmt.Errorf("%w: supersession requires an accepted successor", errTransition)
}
func (s State) decide(act Decision) (State, error) {
	if _, err := s.Value(); err != nil {
		return 0, err
	}
	next := State(0)
	switch act {
	case redraft:
		next = Draft
	case Propose:
		next = proposed
	case Accept:
		next = Accepted
	case reject:
		next = rejected
	case deprecate:
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
		allowed = next == proposed || next == Accepted || next == rejected
	case proposed:
		allowed = next == Draft || next == Accepted || next == rejected
	case Accepted:
		allowed = next == Deprecated || next == Superseded
	case rejected:
		allowed = next == Draft
	case Deprecated:
		allowed = next == Accepted
	}
	if !allowed {
		return 0, fmt.Errorf("%w: %s cannot act %d", errTransition, s, act)
	}
	return next, nil
}

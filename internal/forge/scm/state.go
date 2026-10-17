package scm

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
)

// The run's merge slot records one admitted operation at a time. A new operation
// may follow a recorded merge when further work is explicitly requested.
type State uint8

const (
	Idle State = iota + 1
	Pending
	Recorded
)

var ErrChanged = errors.New("merge operation or its source changed; inspect the source and retry")

func (s State) String() string {
	switch s {
	case Idle:
		return "idle"
	case Pending:
		return "merging"
	case Recorded:
		return "merged"
	default:
		return "invalid-merge-state"
	}
}
func (s State) MarshalJSON() ([]byte, error) {
	value, err := s.Value()
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (s State) Value() (driver.Value, error) {
	if s < Idle || s > Recorded {
		return nil, fmt.Errorf("invalid merge state %d", s)
	}
	return s.String(), nil
}
func (s *State) Scan(value any) error {
	var text string
	switch value := value.(type) {
	case string:
		text = value
	case []byte:
		text = string(value)
	default:
		return fmt.Errorf("invalid merge state value %T", value)
	}
	for candidate := Idle; candidate <= Recorded; candidate++ {
		if candidate.String() == text {
			*s = candidate
			return nil
		}
	}
	return fmt.Errorf("invalid stored merge state %q", text)
}
func (s State) transition(next State) (State, error) {
	if (s == Idle || s == Recorded) && next == Pending || s == Pending && (next == Idle || next == Recorded) {
		return next, nil
	}
	return 0, fmt.Errorf("invalid merge transition %s to %s", s, next)
}

type intent struct {
	State                       State
	Operation, Expected, Result string
}

func (m intent) begin(operation, expected string) (intent, error) {
	next, err := m.State.transition(Pending)
	if err != nil {
		return intent{}, err
	}
	if operation == "" || expected == "" || operation == m.Operation {
		return intent{}, ErrChanged
	}
	return intent{State: next, Operation: operation, Expected: expected}, nil
}
func (m intent) settle(sha string) (intent, error) {
	next, err := m.State.transition(Recorded)
	if err != nil {
		return intent{}, err
	}
	if m.Operation == "" || m.Expected == "" || sha == "" {
		return intent{}, ErrChanged
	}
	m.State, m.Result = next, sha
	return m, nil
}
func (m intent) cancel() (intent, error) {
	next, err := m.State.transition(Idle)
	if err != nil {
		return intent{}, err
	}
	return intent{State: next}, nil
}

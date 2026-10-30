package deployment

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/enum"
)

// ReleaseLabel describes the release record, independently of runtime status.
type ReleaseLabel ReleaseState

func (s ReleaseState) Display() ReleaseLabel { return ReleaseLabel(s) }
func (s ReleaseLabel) String() string {
	switch ReleaseState(s) {
	case Releasing:
		return "deploying"
	case Released:
		return "deployed"
	case ReleaseFailed:
		return "failed"
	default:
		return "unknown"
	}
}
func (s ReleaseLabel) Value() (driver.Value, error) {
	if _, err := ReleaseState(s).Value(); err != nil {
		return nil, err
	}
	return s.String(), nil
}
func (s ReleaseLabel) MarshalJSON() ([]byte, error) {
	if _, err := s.Value(); err != nil {
		return nil, err
	}
	return json.Marshal(s.String())
}
func (s *ReleaseLabel) UnmarshalJSON(data []byte) error {
	var word string
	if err := json.Unmarshal(data, &word); err != nil {
		return err
	}
	for state := Releasing; state <= ReleaseFailed; state++ {
		if state.Display().String() == word {
			*s = state.Display()
			return nil
		}
	}
	return fmt.Errorf("invalid release label %q", word)
}

// DepartureLabel includes a preview that is already running.
type DepartureLabel uint8

const (
	DepartureRunning DepartureLabel = iota + 1
	DepartureDeploying
	DepartureDeployed
	DepartureFailed
)

var departureCodec = enum.Set[DepartureLabel]{Names: []string{"running", "deploying", "deployed", "failed"}, Invalid: "invalid-departure-label", Err: errors.New("invalid departure label"), ScanLabel: "value"}

func (s DepartureLabel) String() string                { return departureCodec.String(s) }
func (s DepartureLabel) Value() (driver.Value, error)  { return departureCodec.Value(s) }
func (s DepartureLabel) MarshalJSON() ([]byte, error)  { return departureCodec.JSON(s) }
func (s *DepartureLabel) UnmarshalJSON(b []byte) error { return departureCodec.ReadJSON(s, b) }
func (s ReleaseState) DepartureLabel() DepartureLabel {
	switch s {
	case Releasing:
		return DepartureDeploying
	case Released:
		return DepartureDeployed
	case ReleaseFailed:
		return DepartureFailed
	default:
		return 0
	}
}

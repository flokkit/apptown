package workflow

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

// personalOpenState is Work's reader-relative classification of open work.
type personalOpenState uint8

const (
	openRunning personalOpenState = iota + 1
	openWaitingReader
	openWaitingElsewhere
	openIdle
)

var personalOpenStateCodec = enum.Set[personalOpenState]{
	Names:   []string{"running", "waiting on you", "waiting on someone else", "idle"},
	Invalid: "invalid-personal-open-state",
	Err:     errors.New("invalid personal open state"),
}

func (s personalOpenState) String() string               { return personalOpenStateCodec.String(s) }
func (s personalOpenState) Value() (driver.Value, error) { return personalOpenStateCodec.Value(s) }
func (s personalOpenState) MarshalJSON() ([]byte, error) { return personalOpenStateCodec.JSON(s) }
func (s *personalOpenState) UnmarshalJSON(data []byte) error {
	return personalOpenStateCodec.ReadJSON(s, data)
}

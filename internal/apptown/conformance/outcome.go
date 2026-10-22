package conformance

import (
	"database/sql/driver"
	"errors"

	"github.com/flokkit/apptown/internal/enum"
)

// Outcome is the immutable result of evaluating one exact project/check/source.
// Currentness and absence are observations, never persisted outcomes.
type Outcome uint8

const (
	Conforms Outcome = iota + 1
	DoesNotConform
	Excepted
	NotApplicable
	CannotAssess
	EvaluationError
)

// outcomeCodec spells each Outcome in storage and JSON.
var outcomeCodec = enum.Set[Outcome]{
	Names:     []string{"conforms", "does-not-conform", "excepted", "not-applicable", "cannot-assess", "error"},
	Invalid:   "invalid-outcome",
	Err:       ErrOutcome,
	ScanLabel: "database value",
}

var ErrOutcome = errors.New("invalid conformance outcome")

func (o Outcome) String() string { return outcomeCodec.String(o) }

func ParseOutcome(text string) (Outcome, error) { return outcomeCodec.Parse(text) }

func (o Outcome) Value() (driver.Value, error) { return outcomeCodec.Value(o) }

func (o *Outcome) Scan(value any) error { return outcomeCodec.Scan(o, value) }

func (o Outcome) MarshalJSON() ([]byte, error) { return outcomeCodec.JSON(o) }

func (o *Outcome) UnmarshalJSON(data []byte) error { return outcomeCodec.ReadJSON(o, data) }

package deployment

import (
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/flokkit/apptown/internal/enum"
)

// Readiness is a deployment's standing against its desired generation:
// reconciled when the observed generation is the desired one, reconciling
// while an attempt is admitted, uncertain when the attempt's runner was lost
// before an outcome, failed when the last attempt ended in failure.
type Readiness uint8

const (
	Reconciled Readiness = iota + 1
	reconciling
	ReadinessUncertain
	ReadinessFailed
)

// readinessCodec spells each Readiness in storage and JSON.
var readinessCodec = enum.Set[Readiness]{
	Names:     []string{"reconciled", "reconciling", "uncertain", "failed"},
	Invalid:   "invalid-readiness",
	Err:       errReadiness,
	ScanLabel: "value",
}

var (
	errReadiness           = errors.New("invalid readiness")
	errReadinessTransition = errors.New("invalid readiness transition")
)

func (s Readiness) String() string { return readinessCodec.String(s) }

// admitted is the rule when a new generation is admitted: whatever stood
// before, the deployment is now reconciling toward it.
func (s Readiness) admitted() (Readiness, error) {
	if s < Reconciled || s > ReadinessFailed {
		return 0, fmt.Errorf("%w: %d", errReadiness, s)
	}
	return reconciling, nil
}

// settled is the rule when an admitted attempt ends: a done attempt makes
// the deployment reconciled, a failed one failed. Only a reconciling or
// uncertain deployment has an attempt to settle.
func (s Readiness) settled(outcome AttemptState) (Readiness, error) {
	if s != reconciling && s != ReadinessUncertain {
		return 0, fmt.Errorf("%w: nothing is being reconciled while %s", errReadinessTransition, s)
	}
	switch outcome {
	case AttemptDone:
		return Reconciled, nil
	case AttemptFailed:
		return ReadinessFailed, nil
	default:
		return 0, fmt.Errorf("%w: an attempt that is %s settles nothing", errReadinessTransition, outcome)
	}
}

// lost is the rule when the runner of an admitted attempt is gone before
// its outcome: the deployment is uncertain until observed.
func (s Readiness) lost() (Readiness, error) {
	switch s {
	case reconciling, ReadinessUncertain:
		return ReadinessUncertain, nil
	default:
		return 0, fmt.Errorf("%w: nothing is being reconciled while %s", errReadinessTransition, s)
	}
}

// doubted is the rule when a deployment's own records contradict each other
// (an attempt still open while it reads reconciled, a release still
// releasing after its attempt ended): whatever it read, its standing is
// uncertain until a person has it observed (Service.CheckAgain).
func (s Readiness) doubted() (Readiness, error) {
	if s < Reconciled || s > ReadinessFailed {
		return 0, fmt.Errorf("%w: %d", errReadiness, s)
	}
	return ReadinessUncertain, nil
}

func (s Readiness) Value() (driver.Value, error) { return readinessCodec.Value(s) }

func (s *Readiness) Scan(value any) error { return readinessCodec.Scan(s, value) }

func (s Readiness) MarshalJSON() ([]byte, error) { return readinessCodec.JSON(s) }

func (s *Readiness) UnmarshalJSON(data []byte) error { return readinessCodec.ReadJSON(s, data) }

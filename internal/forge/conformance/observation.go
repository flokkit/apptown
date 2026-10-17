package conformance

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"flokkit.io/forge/internal/ids"
	"strings"
)

// ObservationState is a closed set: a real outcome or a view-only observation.
// An arbitrary string or another domain's status cannot enter this field.
type ObservationState interface {
	String() string
	observationState()
}

func (Outcome) observationState() {}

type viewState string

func (s viewState) String() string  { return string(s) }
func (viewState) observationState() {}

const (
	Stale      viewState = "stale"
	NotScanned viewState = "not-scanned"
)

var ErrObservationStorage = errors.New("a conformance observation is not a saved verdict")

// Even binding a view-only state directly to SQL is an error. Only an Outcome
// can be stored; obtaining one from an arbitrary string requires validation.
func (viewState) Value() (driver.Value, error) { return nil, ErrObservationStorage }

// CheckRef identifies the currently adopted check, independently of whether any
// evaluation record exists. Project identity survives a rename.
type CheckRef struct {
	ProjectID   ids.ID
	ProjectSlug string
	Block       string
	Version     int
	Check       string
}

func (v Verdict) Reference() CheckRef {
	return CheckRef{v.ProjectID, v.ProjectSlug, v.Block, v.Version, v.Check}
}

// Observation keeps its immutable evidence separate from current interpretation.
// Record is nil when no verdict exists; absence is not an ID-zero saved verdict.
type Observation struct {
	CheckRef
	Record *Verdict
	State  ObservationState
	note   string
}

func Unassessed(ref CheckRef) Observation { return Observation{CheckRef: ref, State: NotScanned} }

func Recorded(v Verdict) Observation {
	return Observation{CheckRef: v.Reference(), Record: &v, State: v.State}
}

func RecordedObservations(vs []Verdict) []Observation {
	out := make([]Observation, 0, len(vs))
	for _, v := range vs {
		out = append(out, Recorded(v))
	}
	return out
}

// CurrentContext describes the adopted check and the source inspected outside
// the database snapshot. Missing source cannot establish a saved result as fresh.
type CurrentContext struct {
	Check             CheckRef
	HeadSHA           string
	SourceUnavailable string
	ExceptionActive   bool
}

func (c CheckRef) Identity() CheckIdentity {
	return CheckIdentity{c.ProjectID, c.Block, c.Version, c.Check}
}

// Observe retains saved evidence, but groups under the current project name and
// interprets source availability, staleness and exception expiry in that order.
func Observe(v Verdict, current CurrentContext) Observation {
	o := Recorded(v)
	o.CheckRef = current.Check
	if current.HeadSHA == "" {
		o.State = CannotAssess
		o.note = current.SourceUnavailable
		if o.note == "" {
			o.note = "current source is unavailable"
		}
	} else if v.SHA != current.HeadSHA || v.Version != current.Check.Version {
		o.State = Stale
	} else if v.State == Excepted && !current.ExceptionActive {
		o.State = DoesNotConform
		o.note = "(the exception that covered this has expired or been removed)"
	}
	return o
}

func (o Observation) Detail() string {
	if o.Record == nil {
		return o.note
	}
	if o.note == "" {
		return o.Record.Detail
	}
	return strings.TrimSpace(o.Record.Detail + "\n" + o.note)
}

// The HTTP representation remains flat, while the internal model distinguishes
// evidence from observation. Only this serialization edge synthesizes the empty
// fields needed to display an unassessed check; that shape is never a Verdict.
func (o Observation) MarshalJSON() ([]byte, error) {
	if o.State == nil {
		return nil, errors.New("missing conformance observation state")
	}
	var evidence Verdict
	if o.Record != nil {
		evidence = *o.Record
	}
	evidence.ProjectID, evidence.ProjectSlug = o.ProjectID, o.ProjectSlug
	evidence.Block, evidence.Version, evidence.Check = o.Block, o.Version, o.Check
	if o.Record == nil {
		evidence.Mode = "posture"
	}
	evidence.Detail = o.Detail()
	return json.Marshal(struct {
		Verdict
		State ObservationState `json:"state"`
	}{evidence, o.State})
}

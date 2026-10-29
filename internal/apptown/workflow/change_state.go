package workflow

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/enum"
	"github.com/flokkit/apptown/internal/stamp"
)

// ChangeState is the lifecycle of a unit of work, from when someone starts it. Zero is
// invalid; a missing observation must be represented separately. The workflow package owns this model and its legal acts.
type ChangeState uint8

const (
	ChangeInProgress ChangeState = iota + 1
	ChangeLanding
	ChangeShipped
	ChangeAbandoned
)

// changeStateCodec spells each ChangeState in storage and JSON.
var changeStateCodec = enum.Set[ChangeState]{
	Names:     []string{"in_progress", "landing", "shipped", "abandoned"},
	Invalid:   "invalid-change-state",
	Err:       errChangeState,
	ScanLabel: "database value",
}

var (
	ErrChangeTransition = errors.New("invalid change transition")
	errChangeState      = errors.New("invalid change state")
)

func (s ChangeState) String() string { return changeStateCodec.String(s) }

func (s ChangeState) Closed() bool { return s == ChangeShipped || s == ChangeAbandoned }
func (s ChangeState) CanRun() bool { return s == ChangeInProgress || s == ChangeLanding }

type ChangeAction uint8

const (
	AttachChangeRun ChangeAction = iota + 1
	LandChange
	ResumeChange
	CancelChangeLanding
	ShipChange
	AbandonChange
)

// transition is the only rule for persisted Change lifecycle changes. Commands
// name an act; callers cannot choose an arbitrary next state. A detached record
// does not authorize a write: the participant reads and guards the current row.
func (s ChangeState) Transition(act ChangeAction) (ChangeState, error) {
	if _, err := s.Value(); err != nil {
		return 0, err
	}
	switch act {
	case AttachChangeRun:
		if s.CanRun() {
			return s, nil
		}
	case LandChange:
		if s.CanRun() {
			return ChangeLanding, nil
		}
	case ResumeChange:
		if s.CanRun() {
			return ChangeInProgress, nil
		}
	case CancelChangeLanding:
		if s == ChangeLanding {
			return ChangeInProgress, nil
		}
		return s, nil // cancelling a run must not reopen a closed Change
	case ShipChange:
		if s.CanRun() {
			return ChangeShipped, nil
		}
	case AbandonChange:
		if !s.Closed() {
			return ChangeAbandoned, nil
		}
	}
	return 0, fmt.Errorf("%w: act %d from %s", ErrChangeTransition, act, s)
}

func ParseChangeState(text string) (ChangeState, error) { return changeStateCodec.Parse(text) }

func (s ChangeState) Value() (driver.Value, error) { return changeStateCodec.Value(s) }

func (s *ChangeState) Scan(value any) error { return changeStateCodec.Scan(s, value) }

func (s ChangeState) MarshalJSON() ([]byte, error) { return changeStateCodec.JSON(s) }

func (s *ChangeState) UnmarshalJSON(data []byte) error {
	return changeStateCodec.ReadJSON(s, data)
}

// closedRecord is Change.ClosedAs for a change a record run (research, a
// specification) shipped: accepting it closed the change with nothing to
// land.
const closedRecord = "record"

// Label is a change's state in the words every reader shows (the CLI, the
// web): one rule, so they cannot disagree. How the change was closed
// (ClosedAs) says what more than its state there is to say.
func (s ChangeState) Label(closedAs string) string {
	switch {
	case s == ChangeShipped && closedAs == ClosedShippedWith:
		return "shipped with a later change"
	case s == ChangeShipped && closedAs == closedRecord:
		return "finished"
	case s == ChangeAbandoned && closedAs == ClosedNothingToChange:
		return "closed: nothing to change"
	}
	return strings.ReplaceAll(s.String(), "_", " ")
}

type Change struct {
	ID          ids.ID      `json:"id"`
	Number      int64       `json:"number"` // within its project
	ProjectID   ids.ID      `json:"projectId"`
	Type        string      `json:"type"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	RunID       ids.ID      `json:"runId"`
	State       ChangeState `json:"state"`
	Branch      string      `json:"branch,omitempty"`
	BaseSHA     string      `json:"baseSha,omitempty"`
	LandedSHA   string      `json:"landedSha,omitempty"` // the commit the landing merged as, written with its merge receipt; "" until landed
	ExternalRef string      `json:"externalRef,omitempty"`
	Summary     string      `json:"summary,omitempty"`
	Provenance  string      `json:"provenance,omitempty"`
	CreatedAt   string      `json:"createdAt"`
	ClosedAt    stamp.Time  `json:"closedAt,omitzero"`
	// ClosedAs is how a closed change was closed, when that is more than its
	// state: ClosedNothingToChange, ClosedShippedWith, closedRecord, or "".
	ClosedAs string `json:"closedAs,omitempty"`
	// StateLabel is State.Label(ClosedAs), filled where the
	// change is read.
	StateLabel string `json:"stateLabel"`
	// Projected from the abandonment receipt, never a second stored reason.
	AbandonReason string `json:"-"`
	// An uplift's target revision, what it must prove, the revision it was
	// prepared from and the one it moved the project to (§11).
	TargetAdoptionID    ids.ID `json:"targetAdoptionId,omitzero"`
	AcceptanceJSON      string `json:"-"`
	TargetCandidateJSON string `json:"-"`
	ExpectedRevision    int64  `json:"expectedRevision,omitempty"`
	AdoptedRevision     int64  `json:"adoptedRevision,omitempty"`
	// EditRevision advances with every human edit of the title, body or
	// external reference; an edit sends the one it started from.
	EditRevision int64 `json:"editRevision"`
	// OpenedBy is who opened the change, as its creation receipt names them;
	// empty when no receipt recorded it.
	OpenedBy string `json:"openedBy,omitempty"`
	// OpenedByPrincipal is the person who opened it, by principal; Nil when
	// the opener was not resolved to a person (AppTown).
	OpenedByPrincipal ids.ID `json:"openedByPrincipal,omitzero"`
	// RequestMet is whether the change does what was asked, as its narrative
	// read the request against the work; RequestGap says what falls short.
	RequestMet RequestMet `json:"requestMet"`
	RequestGap string     `json:"requestGap,omitempty"`
}

// LandsWithoutCode: the change's landing does something besides the merge,
// so a change that needed no code still has something to land. An adoption
// Change does: once the landing gate has judged its target's checks on the
// tree, the landing moves the project to the target it recorded. Only the
// adoption door attaches a target; the type alone is not enough, because
// the ordinary door accepts any type, and such a change moves nothing.
func (c *Change) LandsWithoutCode() bool {
	return c != nil && c.Type == "uplift" && !c.TargetAdoptionID.IsZero()
}

// RequestMet is the narrative writer's reading of the work against the
// request. It is a model's reading, not a check: it can warn a person
// before they approve, never pass or block a gate.
type RequestMet string

const (
	RequestMetYes     RequestMet = "yes"
	RequestMetPartly  RequestMet = "partly"
	RequestMetNo      RequestMet = "no"
	RequestMetUnknown RequestMet = "unknown"
)

// parseRequestMet reads a stored or written answer; anything else is unknown.
func parseRequestMet(text string) RequestMet {
	switch m := RequestMet(strings.ToLower(strings.TrimSpace(text))); m {
	case RequestMetYes, RequestMetPartly, RequestMetNo:
		return m
	}
	return RequestMetUnknown
}

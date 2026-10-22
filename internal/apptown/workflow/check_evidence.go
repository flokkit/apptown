package workflow

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/flokkit/apptown/internal/enum"
)

// CheckVerdict is a check receipt's word for what one check said of the
// commit. Pass, fail, error and cannot-assess are the judge's outcomes; the
// rest are AppTown's: skipped (nothing to judge with), excepted (a person
// already excused what was found), unclear (a model declined to say).
type CheckVerdict uint8

const (
	VerdictPass CheckVerdict = iota + 1
	VerdictFail
	VerdictError
	VerdictCannotAssess
	VerdictSkipped
	VerdictExcepted
	VerdictUnclear
)

var errCheckVerdict = errors.New("invalid check verdict")

var checkVerdictCodec = enum.Set[CheckVerdict]{
	Names:     []string{"pass", "fail", "error", "cannot-assess", "skipped", "excepted", "unclear"},
	Invalid:   "invalid-check-verdict",
	Err:       errCheckVerdict,
	ScanLabel: "value",
}

func (v CheckVerdict) String() string                     { return checkVerdictCodec.String(v) }
func ParseCheckVerdict(text string) (CheckVerdict, error) { return checkVerdictCodec.Parse(text) }

// Value is not for storage (a verdict is never a column): it is how the wire
// generator recognises a word enum and writes its TypeScript union.
func (v CheckVerdict) Value() (driver.Value, error)     { return checkVerdictCodec.Value(v) }
func (v CheckVerdict) MarshalJSON() ([]byte, error)     { return checkVerdictCodec.JSON(v) }
func (v *CheckVerdict) UnmarshalJSON(data []byte) error { return checkVerdictCodec.ReadJSON(v, data) }

// CheckReceipt is what one check said of the commit: the receipt shared by
// the gate, the baseline, the fleet review, the reviewer and the web.
// Pointer scalars distinguish an actual exit 0 / zero duration / non-gating
// judgment from fields that do not apply to an evaluator.
type CheckReceipt struct {
	Scanner      string       `json:"scanner,omitempty"`
	Carried      int          `json:"carried,omitempty"`
	CarriedNote  string       `json:"carriedNote,omitempty"`
	BaseNote     string       `json:"baseNote,omitempty"`
	Block        string       `json:"block"`
	Version      int          `json:"version"`
	Check        string       `json:"check"`
	Evaluator    string       `json:"evaluator"`
	Verdict      CheckVerdict `json:"verdict"`
	ExitCode     *int         `json:"exitCode,omitempty"`
	DurationMs   *int64       `json:"durationMs,omitempty"`
	Output       string       `json:"output,omitempty"`
	Detail       string       `json:"detail,omitempty"`
	Note         string       `json:"note,omitempty"`
	Tool         string       `json:"tool,omitempty"`
	ExceptedBy   string       `json:"exceptedBy,omitempty"`
	Scope        string       `json:"scope,omitempty"`
	Gates        *bool        `json:"gates,omitempty"`
	EvaluatorVer string       `json:"evaluatorVer,omitempty"`
	EvidenceHash string       `json:"evidenceHash,omitempty"`
	Evidence     string       `json:"evidence,omitempty"`
	Files        []string     `json:"files,omitempty"`
	Occurrences  []string     `json:"occurrences,omitempty"`
	// AppTownFault: AppTown could not run the check; the commit is not at fault.
	AppTownFault bool `json:"apptownFault,omitempty"`
	// Preexisting and Undecided: how a failure read against main — already
	// there, or not decidable. Neither is the change's.
	Preexisting bool   `json:"preexisting,omitempty"`
	Undecided   bool   `json:"undecided,omitempty"`
	Image       string `json:"image,omitempty"` // the image (by ID) an exec-image check ran in
}

// UnmarshalJSON reads a receipt as written: evidence recorded before the
// AppTown rename names the platform fault forgeFault, and that recorded fact
// is kept. Receipts are always written as apptownFault.
func (c *CheckReceipt) UnmarshalJSON(data []byte) error {
	type receipt CheckReceipt
	var r struct {
		receipt
		OldFault bool `json:"forgeFault"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	*c = CheckReceipt(r.receipt)
	c.AppTownFault = c.AppTownFault || r.OldFault
	return nil
}

// CheckEvidence is the part of a check step's evidence that owners other
// than its runner judge: the acceptance carry, the check-worker gate, the
// reviewer and the web. It is stored flat beside the runner's own keys, which
// stay the runner's (the extension). Placement is workflow's own protected
// write (placed); a runner never sets it.
type CheckEvidence struct {
	Checks []CheckReceipt `json:"checks,omitempty"`
	// CheckErrors counts checks that could not run: not a pass.
	CheckErrors int `json:"checkErrors,omitempty"`
	// Undecided lists findings the gate could not attribute to the change or
	// to main; the reviewer judges them from the diff.
	Undecided []string   `json:"undecided,omitempty"`
	Placement *Placement `json:"placement,omitempty"`
}

// checkEvidenceKeys are the typed facts' keys: no runner writes them through
// its extension.
var checkEvidenceKeys = []string{"checks", "checkErrors", "undecided", "placement"}

// Outcome encodes the typed facts and the runner's extension as one object.
// An extension key naming a typed fact is an encoding error, which records
// nothing: the facts are written only here.
func (e CheckEvidence) Outcome(status StepState, extension map[string]any) (StepOutcome, error) {
	for _, key := range checkEvidenceKeys {
		if _, ok := extension[key]; ok {
			return StepOutcome{}, fmt.Errorf("check evidence key %q is written through CheckEvidence, not the extension", key)
		}
	}
	fields := make(map[string]any, len(extension)+len(checkEvidenceKeys))
	for k, v := range extension {
		fields[k] = v
	}
	typed, err := json.Marshal(e)
	if err != nil {
		return StepOutcome{}, err
	}
	var facts map[string]json.RawMessage
	if err := json.Unmarshal(typed, &facts); err != nil {
		return StepOutcome{}, err
	}
	for k, v := range facts {
		fields[k] = v
	}
	return EncodeOutcome(status, fields, nil)
}

// Rework is Outcome for a verdict that asks for the stage's rework.
func (e CheckEvidence) Rework(extension map[string]any) (StepOutcome, error) {
	outcome, err := e.Outcome(StatusFail, extension)
	outcome.Rework = true
	return outcome, err
}

// ReadCheckEvidence is the one reader of the typed facts. Empty evidence has
// none. Evidence that is not an object, a typed key of another shape or an
// unknown verdict is unreadable: an error, which each caller turns into its
// own explicit outcome, never into "nothing to report". Other keys are the
// runner's and are ignored.
func ReadCheckEvidence(raw json.RawMessage) (CheckEvidence, error) {
	var e CheckEvidence
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return e, nil
	}
	if data[0] != '{' {
		return e, fmt.Errorf("check evidence is not an object")
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return CheckEvidence{}, fmt.Errorf("unreadable check evidence: %w", err)
	}
	for _, c := range e.Checks {
		if _, err := c.Verdict.Value(); err != nil {
			return CheckEvidence{}, fmt.Errorf("unreadable check evidence: check %q: %w", c.Check, err)
		}
	}
	return e, nil
}

// CheckReading is ReadCheckEvidence's answer for readers outside Go: the
// facts, or why they could not be read. The run snapshot attaches it to each
// step that carries check evidence, so the page judges nothing from raw JSON.
type CheckReading struct {
	CheckEvidence
	Unreadable string `json:"unreadable,omitempty"`
}

func readingOf(raw json.RawMessage) *CheckReading {
	facts, err := ReadCheckEvidence(raw)
	if err != nil {
		return &CheckReading{Unreadable: err.Error()}
	}
	return &CheckReading{CheckEvidence: facts}
}

// carriesCheckEvidence: the step types whose evidence holds check facts —
// the check executor's kinds, and scripts, which record their placement.
func carriesCheckEvidence(kind string) bool { return kind == "script" || isCheckKind(kind) }

// isCheckKind reports whether steps of this type run on the check executor
// and so carry check evidence.
func isCheckKind(kind string) bool { return slices.Contains(checkKinds, kind) }

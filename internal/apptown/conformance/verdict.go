package conformance

import (
	"encoding/json"

	"github.com/flokkit/apptown/internal/ids"
)

// Verdict is one immutable evaluation record. It carries the full identity a
// verdict is keyed by (§2): project, exact SHA, expectation version, check and
// evaluator version, evaluation mode, and timestamp.
type Verdict struct {
	ID           ids.ID  `json:"id"`
	ReviewID     ids.ID  `json:"reviewId"`
	ProjectID    ids.ID  `json:"projectId"`
	ProjectSlug  string  `json:"projectSlug"`
	SHA          string  `json:"sha"`
	Block        string  `json:"block"` // the adopted guidance block (the expectation's home)
	Version      int     `json:"version"`
	Check        string  `json:"check"`
	Evaluator    string  `json:"evaluator"`
	EvaluatorVer string  `json:"evaluatorVer"`
	Mode         string  `json:"mode"`
	State        Outcome `json:"state"`
	Severity     string  `json:"severity"`
	Detail       string  `json:"detail"`
	At           string  `json:"at"`
	// ExceptedBy names the exception behind an `excepted` verdict (JSON: the
	// disposition id, status, by); Detail keeps the failure underneath so an
	// expired exception reveals the original finding without re-evaluation.
	ExceptedBy string `json:"exceptedBy,omitempty"`
	// EvidenceHash is the hash of the exact evidence bundle an AI verdict
	// judged (design/subsystems/conformance-surface.md §9 item 4).
	EvidenceHash string `json:"evidenceHash,omitempty"`
}

// CheckIdentity is the stable adopted subject of an evaluation. Display names
// and source SHAs are evidence/metadata, not the identity of the check.
type CheckIdentity struct {
	ProjectID ids.ID
	Block     string
	Version   int
	Check     string
}

func (v Verdict) identity() CheckIdentity {
	return CheckIdentity{v.ProjectID, v.Block, v.Version, v.Check}
}

// VerdictFields has the saved evidence fields without serialization methods.
type VerdictFields Verdict

// VerdictView adds presentation without changing immutable evidence identity.
type VerdictView struct {
	VerdictFields
	StatusLabel CheckLabel `json:"statusLabel"`
}

func (v Verdict) MarshalJSON() ([]byte, error) {
	return json.Marshal(VerdictView{VerdictFields(v), v.State.label()})
}

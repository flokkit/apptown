package conformance

import (
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"
)

// Fix records the evidence and attribution of a verified project repair.
type Fix struct {
	ID              ids.ID   `json:"id"`
	ProjectID       ids.ID   `json:"projectId"`
	CheckKey        string   `json:"checkKey"`
	Check           string   `json:"check"`
	BeforeVerdictID ids.ID   `json:"beforeVerdictId"`
	AfterVerdictID  ids.ID   `json:"afterVerdictId"`
	BeforeSHA       string   `json:"beforeSha"`
	AfterSHA        string   `json:"afterSha"`
	BeforeState     Outcome  `json:"beforeState"`
	BeforeDetail    string   `json:"beforeDetail,omitempty"`
	ReviewID        ids.ID   `json:"reviewId"`
	ReviewNumber    int64    `json:"reviewNumber"` // within AppTown
	ChangeIDs       []ids.ID `json:"changeIds"`
	// ChangeNumbers are ChangeIDs' numbers within the project, index for
	// index; 0 for a change that no longer exists.
	ChangeNumbers   []int64 `json:"changeNumbers"`
	Commits         string  `json:"commits,omitempty"`
	RemediationItem ids.ID  `json:"remediationItemId,omitzero"`
	AcceptedBy      string  `json:"acceptedBy,omitempty"`
	// AcceptedByPrincipal is the accepter's principal; zero when not resolved to a person.
	AcceptedByPrincipal ids.ID   `json:"acceptedByPrincipal,omitzero"`
	Also                []string `json:"also,omitempty"` // other checks the same change flipped
	At                  string   `json:"at"`
}

// FixedBy says whether these immutable outcomes can constitute a repair.
// Human confirmation and attribution are admitted atomically by the store.
func (v Verdict) FixedBy(next Verdict) bool {
	return !v.ID.IsZero() && v.Identity() == next.Identity() &&
		next.State == Conforms && (v.State == DoesNotConform || v.State == Excepted)
}

// Confirmable marks AI findings that require an explicit human confirmation.
func (v Verdict) Confirmable() bool { return v.State == DoesNotConform && v.Evaluator == "llm" }

// FixSentence describes the recorded repair without re-deriving its attribution.
func FixSentence(f Fix) string {
	var by string
	switch len(f.ChangeIDs) {
	case 0:
		by = "by commits " + f.Commits
	case 1:
		by = "by change " + changeName(f, 0)
	default:
		parts := make([]string, len(f.ChangeIDs))
		for i := range f.ChangeIDs {
			parts[i] = changeName(f, i)
		}
		by = "by one of changes " + strings.Join(parts, ", ")
	}
	review := fmt.Sprint(f.ReviewID)
	if f.ReviewNumber > 0 {
		review = fmt.Sprint(f.ReviewNumber)
	}
	s := fmt.Sprintf("%s — fixed %s, verified by conformance review %s", f.Check, by, review)
	if f.AcceptedBy != "" {
		s += ", accepted by " + f.AcceptedBy
	}
	return s
}

// changeName is the i-th attributed change as people name it: its number,
// or its id when the change no longer exists.
func changeName(f Fix, i int) string {
	if i < len(f.ChangeNumbers) && f.ChangeNumbers[i] > 0 {
		return fmt.Sprint(f.ChangeNumbers[i])
	}
	return fmt.Sprint(f.ChangeIDs[i])
}

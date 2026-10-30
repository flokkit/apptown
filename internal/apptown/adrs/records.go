package adrs

import "github.com/flokkit/apptown/internal/ids"

// Record is a detached decision snapshot, not authority to edit it.
type Record struct {
	StatusLabel  ADRLabel `json:"statusLabel"`
	ID           ids.ID   `json:"id"`
	ProjectID    ids.ID   `json:"projectId"`
	ProjectSlug  string   `json:"projectSlug,omitempty"`
	ProjectName  string   `json:"projectName,omitempty"`
	Number       int64    `json:"number"`
	Title        string   `json:"title"`
	Status       State    `json:"status"`
	BodyMD       string   `json:"bodyMd"`
	Tags         string   `json:"tags"`
	SupersedesID ids.ID   `json:"supersedesId"`
	ChangeID     ids.ID   `json:"changeId"`
	ChangeNumber int64    `json:"changeNumber"` // within the project; 0 when no change
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
}
type Detail struct {
	ADR            Record  `json:"adr"`
	SupersededByID ids.ID  `json:"supersededById"`
	Options        Options `json:"options"`
}
type Create struct {
	ProjectID           ids.ID
	Title, BodyMD, Tags string
	ChangeID            ids.ID
}
type Edit struct {
	ID                  ids.ID
	Title, BodyMD, Tags *string
	// Expected is the text the edit started from, for each text field it
	// writes. It is required for those fields: an ADR keeps no versions, so
	// an edit that never saw a newer text would lose it for good.
	Expected TextFields
	ChangeID *ids.ID
	Decision Decision
}

// TextFields are an ADR's editable text, each present only when named.
type TextFields struct {
	Title, BodyMD, Tags *string
}
type Successor struct {
	ID            ids.ID
	Title, BodyMD string
}
type Filter struct {
	ProjectID               ids.ID
	ProjectSlug, Query, Tag string
	State                   State
	ChangeID                ids.ID
	Recent                  bool
	Limit                   int
}
type Match struct {
	ID     ids.ID `json:"id"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
}
type MatchRequest struct {
	ProjectID, ChangeID ids.ID
	State               State
	Tags                []string
}
type ChangeSummary struct {
	StatusLabel ADRLabel `json:"statusLabel"`
	ID          ids.ID   `json:"id"`
	Number      int64    `json:"number"`
	Title       string   `json:"title"`
	Status      State    `json:"status"`
	Tags        string   `json:"tags"`
}

// Options describe lifecycle eligibility. Commands still require current authority
// and fresh state; these observations never authorize a future edit.
type Options struct {
	ContentEditable bool    `json:"contentEditable"`
	CanSupersede    bool    `json:"canSupersede"`
	Transitions     []State `json:"transitions"`
}

func (s State) options() Options {
	out := Options{Transitions: []State{}}
	if _, err := s.Value(); err != nil {
		return out
	}
	out.ContentEditable, out.CanSupersede = !s.Locked(), s == Accepted
	for _, act := range []Decision{Propose, Accept, Reject, Redraft, Deprecate} {
		if next, err := s.decide(act); err == nil && next != s {
			out.Transitions = append(out.Transitions, next)
		}
	}
	return out
}

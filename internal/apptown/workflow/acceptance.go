package workflow

import "encoding/json"

type AcceptanceCheck struct {
	Block   string `json:"block"`
	Check   string `json:"check"`
	Version int    `json:"version,omitempty"`
	Title   string `json:"title,omitempty"`
	Expects string `json:"expects,omitempty"`
}

// Acceptance is stored on an uplift change: what it must prove.
type Acceptance struct {
	TargetDigest string            `json:"targetDigest"`
	Checks       []AcceptanceCheck `json:"checks"`
	// InAdvance is the opener's yes given in advance, when they gave one
	// (in_advance.go).
	InAdvance *AdvanceYes `json:"inAdvance,omitempty"`
}

// acceptance reads what an uplift Change must prove; an empty record is the
// zero Acceptance.
func (c *Change) acceptance() (Acceptance, error) {
	var acc Acceptance
	if c == nil || c.AcceptanceJSON == "" || c.AcceptanceJSON == "{}" {
		return acc, nil
	}
	err := json.Unmarshal([]byte(c.AcceptanceJSON), &acc)
	return acc, err
}

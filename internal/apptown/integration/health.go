package integration

import "time"

// Health is one reading of an integration. Checked says whether AppTown
// has a check for the kind and ran it; CheckedAt is the server's time when
// it did. An unchecked reading is not evidence either way, so OK and Detail
// are empty: "no check" must never read as "working".
type Health struct {
	StateLabel HealthStatus `json:"stateLabel"`
	OK         bool         `json:"ok"`
	Detail     string       `json:"detail"`
	Checked    bool         `json:"checked"`
	CheckedAt  string       `json:"checkedAt,omitempty"`
}

// At is the reading as one AppTown took at the given time.
func (h Health) At(at time.Time) Health {
	h.Checked = true
	h.CheckedAt = at.UTC().Format(time.RFC3339)
	return h.ForDisplay()
}

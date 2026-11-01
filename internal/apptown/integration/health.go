package integration

import "time"

// Health is one reading of an integration. NotSetUp distinguishes entirely absent
// configuration from a failed check. Partial required configuration is a failure. Checked says whether AppTown
// has a check for the kind and ran it; CheckedAt is the server's time when
// it did. A kind with no check has empty OK and Detail; NotSetUp can name
// what is missing. Neither state is evidence that a connection works.
type Health struct {
	Automatic  bool         `json:"automatic"`
	Manual     bool         `json:"manual,omitempty"`
	LastResult HealthStatus `json:"lastResult,omitempty"`
	Stale      bool         `json:"stale,omitempty"`
	NotSetUp   bool         `json:"-"`
	StateLabel HealthStatus `json:"stateLabel"`
	OK         bool         `json:"ok"`
	Detail     string       `json:"detail"`
	Checked    bool         `json:"checked"`
	CheckedAt  string       `json:"checkedAt,omitempty"`
}

// At is the reading as one AppTown took at the given time.
func (h Health) At(at time.Time) Health {
	if h.NotSetUp {
		return h.ForDisplay()
	}
	h.Checked = true
	h.CheckedAt = at.UTC().Format(time.RFC3339)
	return h.ForDisplay()
}

// HasAutomaticCheck identifies kinds whose check is renewed by the collection job.
// Manual-only integrations retain their last test without claiming coverage.
func (b Binding) HasAutomaticCheck() bool {
	switch b.Capability {
	case CapSCM, CapSecrets, CapExecutor, CapModel, CapContainerRegistry:
		return true
	case CapDeploy:
		return b.Provider == "docker"
	default:
		return false
	}
}

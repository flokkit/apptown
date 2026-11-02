package integration

import (
	"encoding/json"
	"time"
)

// Health is one reading of an integration. NotSetUp distinguishes entirely absent
// configuration from a failed check. Partial required configuration is a failure. Checked says whether AppTown
// has a check for the kind and ran it; CheckedAt is the server's time when
// it did. A kind with no check has empty OK and Detail; NotSetUp can name
// what is missing. Neither state is evidence that a connection works.
type Health struct {
	LastGood   *Health      `json:"lastGood,omitempty"`
	FailedAt   string       `json:"failedAt,omitempty"`
	Failure    string       `json:"failure,omitempty"`
	Automatic  bool         `json:"automatic"`
	LastResult HealthStatus `json:"lastResult,omitempty"`
	Expired    bool         `json:"expired,omitempty"`
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
// Kinds without a meaningful check provide no Attention coverage.
func (b Binding) HasAutomaticCheck() bool {
	switch b.Capability {
	case CapFeedback, CapSCM, CapSecrets, CapExecutor, CapModel, CapContainerRegistry, CapRegistry, CapErrors, CapLLM, "cloud-account":
		return true
	case "scanner":
		var config map[string]any
		if err := json.Unmarshal([]byte(b.ConfigJSON), &config); err != nil {
			// The check says the configuration could not be read.
			return true
		}
		return !ScannerRunsInAppImage(config)
	case CapDeploy:
		return b.Provider == "docker"
	default:
		return false
	}
}

// ScannerRunsInAppImage is a scanner that runs inside the app's own image
// (target exec-image): it has no image of its own, so its connection check,
// which asks for the scanner's image, has nothing to ask. Its checks run
// at the land gate, where the app's image is built.
func ScannerRunsInAppImage(config map[string]any) bool {
	target, _ := config["target"].(string)
	return target == "exec-image"
}

// HealthCheckTimeout includes the scanner's first image pull.
func (b Binding) HealthCheckTimeout() time.Duration {
	if b.Capability == "scanner" {
		return 95 * time.Second
	}
	return 25 * time.Second
}

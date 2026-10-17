package workflow

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
}

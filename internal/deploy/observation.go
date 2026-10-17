package deploy

// Observation records what a provider sees for a deployment generation.
type Observation struct {
	ImageDigest string   `json:"imageDigest,omitempty"` // Actual image when execution built it from admitted source.
	Generation  int64    `json:"generation"`
	Rollout     string   `json:"rollout"` // pending | rolling | active | failed | absent
	Health      string   `json:"health"`  // healthy | unhealthy | unknown
	URL         string   `json:"url"`
	Handle      any      `json:"handle,omitempty"`
	Orphans     []string `json:"orphans,omitempty"`
}

// Checkpoint durably records provider identities before execution can proceed.
// Implementations reject stale ownership and conflicting identity updates.
type Checkpoint func(map[string]any) error

package deploy

import "time"

// Observation records what a provider sees for a deployment generation.
type Observation struct {
	// HealthObservedAt and ArtifactHash identify a fresh exact-release health
	// witness. Zero/empty means the provider has supplied no such evidence.
	HealthObservedAt time.Time `json:"healthObservedAt,omitempty"`
	ArtifactHash     string    `json:"artifactHash,omitempty"`
	ImageDigest      string    `json:"imageDigest,omitempty"` // Actual image when execution built it from admitted source.
	Generation       int64     `json:"generation"`
	Rollout          string    `json:"rollout"` // pending | rolling | active | failed | absent
	Health           string    `json:"health"`  // healthy | unhealthy | unknown
	URL              string    `json:"url"`
	Handle           any       `json:"handle,omitempty"`
	Orphans          []string  `json:"orphans,omitempty"`
	// Running is the provider's own name for what runs, read from the same
	// answer as the rest: App Platform's active deployment id, a Portainer
	// stack's app image. Empty when the provider named none (nothing
	// deployed yet, or the app is gone). After a restore it says whether the
	// deployment still runs what AppTown recorded.
	Running string `json:"running,omitempty"`
}

// Checkpoint durably records provider identities before execution can proceed.
// Implementations reject stale ownership and conflicting identity updates.
type Checkpoint func(map[string]any) error

// RunningRecord is a driver whose provider names what runs
// (Observation.Running), so that after AppTown was restored its record can
// be held to the provider's answer. RecordedRunning is what the record says
// runs, in the same terms: from the placement, or else released, what the
// newest released release recorded (its external reference). AdoptRunning
// is what the placement's handle becomes once running is taken as the
// record (nil: nothing).
type RunningRecord interface {
	RecordedRunning(p Placement, released string) string
	AdoptRunning(running string) map[string]any
}

package deploy

import (
	"context"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// Observation records what a provider sees for a deployment generation.
type Observation struct {
	// RuntimeToken is an ephemeral probe incarnation (container ID and start
	// time, or provider deployment ID). It is never restored from a receipt.
	RuntimeToken string `json:"-"`
	// ReadinessPolicy comes from the admitted artifact, never a saved observation.
	ReadinessPolicy *appcontract.HealthPolicy `json:"-"`
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
	// Parts is what runs each part and need, every generation of it the
	// provider lists, read from the same answer, from a provider with no
	// Docker daemon AppTown can ask. Nil: the provider does not report
	// them. Never stored in a receipt.
	Parts []PartRuntime `json:"-"`
}

// PartRuntime is one part's or need's runtime as its provider reports it.
type PartRuntime struct {
	// Part is the part's or need's name; "" when the provider cannot say
	// which it runs (its host cannot be reached and it named none before).
	Part   string
	ID     string // the provider's id for what runs it (a Fly Machine)
	Status string // the provider's word: started, stopped, ...
	// Running is true only when the provider says it runs.
	Running bool
	// Unreachable: the provider cannot reach its host, so what it does
	// now is not known.
	Unreachable bool
	Health      string // healthy, unhealthy, or "" when it has no checks
	Image       string
	StartedAt   time.Time // zero: not reported
	// Generation is the part's generation it runs; 0 when it carries none:
	// a need, which serves every generation.
	Generation  int64
	CPULimit    float64 // the CPUs it is sized to
	CPUKind     string  // the provider's kind of those CPUs ("shared" on Fly); "" when it names none
	MemoryLimit int64   // the memory it is sized to, in bytes
	Exits       []PartExit
	// Use is what it uses now, nil when not read: Observe never reads it,
	// and UseReason says why a UsageReader could not.
	Use       *PartUse
	UseReason string
}

// PartExit is one exit the provider reports. Requested: a stop someone
// asked for (a deploy replacing it, a restart), not the process ending.
type PartExit struct {
	At        time.Time
	Code      int
	OOM       bool
	Requested bool
}

// PartUse is a part's CPU and memory use, sampled once.
type PartUse struct {
	CPUCores    float64
	MemoryBytes int64
}

// UsageReader is a driver that reads each part's use now: the parts as
// Observe reports them, each with its Use or why it is unknown. It is
// asked on the detail reader's cadence, not every observation.
type UsageReader interface {
	ReadUsage(ctx context.Context, p Placement) ([]PartRuntime, error)
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

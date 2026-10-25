package deploy

import "github.com/flokkit/apptown/internal/ids"

import "github.com/flokkit/apptown/internal/stamp"

// Resource is one thing at a target, in the same words whatever the
// provider. A driver reports what it sees; the caller lays its records over it.
type Resource struct {
	// ObservedDaemonID belongs to this exact Docker or Portainer inventory snapshot, including cached observations.
	ObservedDaemonID string   `json:"-"`
	Kind             string   `json:"kind"` // app | database | registry | container | volume | network | image | stack | bucket
	Name             string   `json:"name"`
	ID               string   `json:"id,omitempty"`
	Status           string   `json:"status,omitempty"`
	Region           string   `json:"region,omitempty"`
	Size             string   `json:"size,omitempty"`
	Version          string   `json:"version,omitempty"` // engine + version for a database
	URL              string   `json:"url,omitempty"`
	Detail           string   `json:"detail,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	// What the caller knows about it.
	Record   string         `json:"record"`             // RecordRecorded | RecordUnrecorded | RecordMissing
	Owner    string         `json:"owner,omitempty"`    // AppTown.owner label: which stack made it
	Stack    string         `json:"stack,omitempty"`    // the compose project label: which stack it belongs to
	Hosts    []ResourceHost `json:"hosts,omitempty"`    // the deployments running on / using it
	Retained *RetainedNote  `json:"retained,omitempty"` // left behind by a torn-down deployment
}

// The three things a caller can say about a resource's record.
const (
	RecordRecorded   = "recorded"   // the records say it is there
	RecordUnrecorded = "unrecorded" // the provider has it, the records do not
	RecordMissing    = "missing"    // the records have it, the provider does not
)

// TagConfirmsOnly marks a provider item reported only so a record can be
// confirmed: something AppTown did not name (an approved older volume)
// that is no orphan of AppTown's when nothing records it.
const TagConfirmsOnly = "confirms-only"

// ResourceHost is a deployment that runs on or uses a resource.
type ResourceHost struct {
	Project    string `json:"project"`
	Deployment string `json:"deployment"`
	Generation int64  `json:"generation,omitempty"`
	Need       string `json:"need,omitempty"` // for a database: which need it fulfils
}

// RetainedNote says a resource was left behind by a torn-down deployment.
type RetainedNote struct {
	ID         ids.ID     `json:"id"`
	Project    string     `json:"project"`
	Deployment string     `json:"deployment"`
	RetainedBy string     `json:"retainedBy"`
	RetainedAt stamp.Time `json:"retainedAt"`
	// Reason, when the note comes from a label rather than the retained
	// ledger: why the deployment no longer records the resource.
	Reason string `json:"reason,omitempty"`
}

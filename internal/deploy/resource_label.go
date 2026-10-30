package deploy

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"github.com/flokkit/apptown/internal/enum"
)

// ResourceLabel translates provider inventory states into presentation words.
// The provider's exact state remains in Resource.Status for the record.
type ResourceLabel uint8

const (
	ResourceUnknown ResourceLabel = iota + 1
	ResourceRunning
	ResourceStopped
	ResourceWorking
	ResourceFailed
	ResourcePassed
)

var resourceLabelCodec = enum.Set[ResourceLabel]{Names: []string{"unknown", "running", "stopped", "working", "failed", "passed"}, Invalid: "invalid-resource-label", Err: errors.New("invalid resource label"), ScanLabel: "value"}

func (s ResourceLabel) String() string                { return resourceLabelCodec.String(s) }
func (s ResourceLabel) Value() (driver.Value, error)  { return resourceLabelCodec.Value(s) }
func (s ResourceLabel) MarshalJSON() ([]byte, error)  { return resourceLabelCodec.JSON(s) }
func (s *ResourceLabel) UnmarshalJSON(b []byte) error { return resourceLabelCodec.ReadJSON(s, b) }
func (r Resource) Display() ResourceLabel {
	switch r.Status {
	case "active", "online", "running":
		return ResourceRunning
	case "ready", "reconciled":
		return ResourcePassed
	case "exited", "absent", "stopped":
		return ResourceStopped
	case "deploying", "creating", "reconciling", "rolling":
		return ResourceWorking
	case "error", "failed":
		return ResourceFailed
	default:
		return ResourceUnknown
	}
}

type ResourceFields Resource

// ResourceView keeps the provider fact and its display classification together.
type ResourceView struct {
	ResourceFields
	StatusLabel ResourceLabel `json:"statusLabel"`
}

func (r Resource) MarshalJSON() ([]byte, error) {
	return json.Marshal(ResourceView{ResourceFields(r), r.Display()})
}

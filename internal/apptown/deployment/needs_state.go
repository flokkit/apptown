package deployment

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/deployment/internal/deploymentsql"
	"github.com/flokkit/apptown/internal/enum"
)

// needState is a managed need instance's standing: creating while the
// provider makes it, ready once it answers, failed when the provider refused,
// orphaned when its deployment went before the record did.
type needState uint8

const (
	needCreating needState = iota + 1
	NeedReady
	needFailed
	needOrphaned
)

// needStateCodec spells each NeedState in storage and JSON.
var needStateCodec = enum.Set[needState]{
	Names:     []string{"creating", "ready", "failed", "orphaned"},
	Invalid:   "invalid-need-state",
	Err:       errNeedState,
	ScanLabel: "value",
}

var errNeedState = errors.New("invalid need state")

func (s needState) String() string { return needStateCodec.String(s) }

func (s needState) Value() (driver.Value, error) { return needStateCodec.Value(s) }

func (s *needState) Scan(value any) error { return needStateCodec.Scan(s, value) }

func (s needState) MarshalJSON() ([]byte, error) { return needStateCodec.JSON(s) }

// NeedInstance is one managed need as recorded for a deployment.
type NeedInstance struct {
	Need   string
	Mode   string
	Handle map[string]any
	Status needState
}

// ForDeployment lists a deployment's need instances by need.
func (s NeedStore) ForDeployment(ctx context.Context, deploymentID ids.ID) ([]NeedInstance, error) {
	rows, err := deploymentsql.New(s.db.SQL()).NeedInstances(ctx, []ids.ID{deploymentID})
	out := []NeedInstance{}
	if err != nil {
		return []NeedInstance{}, err
	}
	for _, r := range rows {
		n := NeedInstance{Need: r.Need, Mode: r.Mode, Handle: jsonMap(string(r.HandleJson))}
		if err := n.Status.Scan(r.Status); err != nil {
			return []NeedInstance{}, err
		}
		out = append(out, n)
	}
	return out, nil
}

// Recorded is one need's recorded handle, nil when it has none.
func (s NeedStore) Recorded(ctx context.Context, deploymentID ids.ID, need string) (map[string]any, error) {
	all, err := s.ForDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	for _, n := range all {
		if n.Need == need {
			return n.Handle, nil
		}
	}
	return nil, nil
}

// recordFulfilled records a managed need the provider now answers for, with
// its binding and handle: ready, whatever it was.
func (s NeedStore) recordFulfilled(ctx context.Context, deploymentID ids.ID, need string, binding, handle map[string]any) error {
	now := s.now()
	_, err := deploymentsql.New(s.db.SQL()).RecordNeedFulfilled(ctx, deploymentsql.RecordNeedFulfilledParams{ID: ids.New(now),
		DeploymentID: deploymentID,
		Need:         need,
		BindingJson:  json.RawMessage(jsonText(binding)),
		HandleJson:   json.RawMessage(jsonText(handle)),
		Status:       NeedReady.String(),
		UpdatedAt:    now,
	})
	return err
}

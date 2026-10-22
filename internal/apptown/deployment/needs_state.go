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

// NeedState is a managed need instance's standing: creating while the
// provider makes it, ready once it answers, failed when the provider refused,
// orphaned when its deployment went before the record did.
type NeedState uint8

const (
	NeedCreating NeedState = iota + 1
	NeedReady
	NeedFailed
	NeedOrphaned
)

// needStateCodec spells each NeedState in storage and JSON.
var needStateCodec = enum.Set[NeedState]{
	Names:     []string{"creating", "ready", "failed", "orphaned"},
	Invalid:   "invalid-need-state",
	Err:       ErrNeedState,
	ScanLabel: "value",
}

var ErrNeedState = errors.New("invalid need state")

func (s NeedState) String() string { return needStateCodec.String(s) }

func (s NeedState) Value() (driver.Value, error) { return needStateCodec.Value(s) }

func (s *NeedState) Scan(value any) error { return needStateCodec.Scan(s, value) }

func (s NeedState) MarshalJSON() ([]byte, error) { return needStateCodec.JSON(s) }

// NeedInstance is one managed need as recorded for a deployment.
type NeedInstance struct {
	Need   string
	Mode   string
	Handle map[string]any
	Status NeedState
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

// RecordFulfilled records a managed need the provider now answers for, with
// its binding and handle: ready, whatever it was.
func (s NeedStore) RecordFulfilled(ctx context.Context, deploymentID ids.ID, need string, binding, handle map[string]any) error {
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

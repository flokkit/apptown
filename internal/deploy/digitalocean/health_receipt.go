package digitalocean

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/ids"
)

// healthReceipt retains only admitted observation inputs. Completed attempts
// replace their checkpoints, so this travels in the final provider Handle too.
type healthReceipt struct {
	Version    int               `json:"version"`
	Connection ids.ID            `json:"connection"`
	APIBase    string            `json:"apiBase"`
	App        string            `json:"app"`
	Deployment string            `json:"deployment"`
	Generation int64             `json:"generation"`
	Artifact   string            `json:"artifact"`
	Readiness  readinessSnapshot `json:"readiness"`
	Release    string            `json:"release"`
	Components []string          `json:"components"`
}

func (d *Driver) newHealthReceipt(c *Client, p deploy.Placement, generation int64, art deploy.Artifact, decoded decodedAppArtifact) (*healthReceipt, error) {
	// Historical artifacts and unassociated library callers retain deployment
	// behavior, but cannot establish maintained-health provenance.
	if p.Gate != nil || d.ConnectionID.IsZero() || art.Kind != appSpecV2 {
		return nil, nil
	}
	r := &healthReceipt{Version: 1, Connection: d.ConnectionID, APIBase: strings.TrimRight(c.Base, "/"), Generation: generation, Artifact: art.Hash, Readiness: decoded.Readiness, Release: expectedRelease(art.Source)}
	for _, components := range [][]Component{decoded.Spec.Services, decoded.Spec.Workers} {
		for _, component := range components {
			r.Components = append(r.Components, component.Name)
		}
	}
	sort.Strings(r.Components)
	if err := r.validateInput(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *healthReceipt) validate() error {
	if r.App == "" || r.Deployment == "" {
		return errors.New("recorded App Platform health receipt has no provider identity")
	}
	return r.validateInput()
}

func (r *healthReceipt) validateInput() error {
	if r.Version != 1 || r.Connection.IsZero() || r.APIBase == "" || r.Generation <= 0 || r.Artifact == "" || len(r.Components) == 0 {
		return errors.New("recorded App Platform health receipt is incomplete")
	}
	raw, err := json.Marshal(r.Readiness)
	if err != nil {
		return err
	}
	if _, err = decodeReadiness(raw); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, name := range r.Components {
		if name == "" || names[name] {
			return errors.New("recorded App Platform health component identity is invalid")
		}
		names[name] = true
	}
	return nil
}

func readHealthReceipt(handle map[string]any) (*healthReceipt, error) {
	value, present := handle["healthReceipt"]
	if !present {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var r healthReceipt
	if err := strictRead(raw, &r); err != nil {
		return nil, err
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	if handle["appId"] != r.App || handle["deploymentId"] != r.Deployment {
		return nil, errors.New("recorded App Platform health receipt disagrees with provider identities")
	}
	return &r, nil
}

func (d *Driver) receiptConnection(r *healthReceipt, generation int64) error {
	if r.Generation != generation || d.ConnectionID.IsZero() || r.Connection != d.ConnectionID {
		return errors.New("recorded App Platform health generation or connection changed")
	}
	return nil
}

func receiptClient(r *healthReceipt, c *Client) error {
	if strings.TrimRight(c.Base, "/") != r.APIBase {
		return errors.New("recorded App Platform health API destination changed")
	}
	return nil
}

func retainHealthReceipt(observed deploy.Observation, err error, r *healthReceipt) (deploy.Observation, error) {
	if err == nil && r != nil {
		handle, ok := observed.Handle.(map[string]any)
		if !ok {
			return deploy.Observation{}, errors.New("completed App Platform observation has no provider handle")
		}
		handle["healthReceipt"] = *r
	}
	return observed, err
}

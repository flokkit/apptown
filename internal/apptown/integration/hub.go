package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/integration/internal/integrationsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

// HubPolicy keeps the age window and newest bundles together, not alternatives.
type HubPolicy struct {
	KeepDays     int `json:"keepDays"`
	KeepReleases int `json:"keepReleases"`
}

func hubPolicy(raw string) (HubPolicy, error) {
	policy := HubPolicy{KeepDays: 90, KeepReleases: 10}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return policy, err
	}
	if policy.KeepDays < 1 || policy.KeepReleases < 1 {
		return policy, fmt.Errorf("keepDays and keepReleases must be positive whole numbers")
	}
	return policy, nil
}

func validateHubConfig(capability, provider, raw string) error {
	if capability != CapContainerRegistry {
		return nil
	}
	cfg, err := strictJSONMap(raw)
	if err != nil {
		return err
	}
	if provider == "gitlab" || cfg["provider"] == "gitlab" {
		return fault.New(fault.Unprocessable, "the hub needs one registry credential for all projects; GitLab per-project setup is unavailable")
	}
	_, err = hubPolicy(raw)
	return err
}

func checkOneHub(ctx context.Context, tx *database.Tx, name, capability string) error {
	if capability != CapContainerRegistry {
		return nil
	}
	// Serialize connector creation, including the empty-table case.
	if err := integrationsql.New(tx.SQL()).LockHubRegistry(ctx); err != nil {
		return err
	}
	hubs, err := ForCapability(ctx, tx, capability)
	if err != nil {
		return err
	}
	for _, hub := range hubs {
		if hub.Name != name {
			return fault.New(fault.Conflict, "an AppTown has one hub registry")
		}
	}
	return nil
}

// Hub returns the current connector and its validated retention policy.
func (s *Service) Hub(ctx context.Context) (*Binding, HubPolicy, error) {
	hubs, err := ForCapability(ctx, s.Bindings.db, CapContainerRegistry)
	if err != nil {
		return nil, HubPolicy{}, err
	}
	if len(hubs) == 0 {
		return nil, HubPolicy{}, nil
	}
	if len(hubs) != 1 {
		return nil, HubPolicy{}, fmt.Errorf("an AppTown has one hub registry")
	}
	policy, err := hubPolicy(hubs[0].ConfigJSON)
	return &hubs[0], policy, err
}

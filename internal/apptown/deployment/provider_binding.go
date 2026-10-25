package deployment

import (
	"encoding/json"
	"errors"

	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/ids"
)

type providerBinding struct {
	ConnectionID ids.ID          `json:"connectionId"`
	Scope        portainer.Scope `json:"scope"`
}

func providerBindingOf(target *Target) (*json.RawMessage, error) {
	if target.Kind != KindPortainer {
		return nil, nil
	}
	if target.ConnectionID.IsZero() || len(target.ProviderScope) == 0 {
		return nil, errors.New("Portainer release needs a verified provider association")
	}
	binding := providerBinding{ConnectionID: target.ConnectionID}
	if err := json.Unmarshal(target.ProviderScope, &binding.Scope); err != nil {
		return nil, err
	}
	if binding.Scope.Version != 1 || binding.Scope.APIBase == "" || binding.Scope.DaemonID == "" || binding.Scope.EndpointID <= 0 {
		return nil, errors.New("Portainer provider association is incomplete")
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(encoded)
	return &raw, nil
}

func matchesProviderBinding(target *Target, saved *json.RawMessage) error {
	if target.Kind != KindPortainer {
		if saved != nil {
			return errors.New("release target provider kind changed")
		}
		return nil
	}
	current, err := providerBindingOf(target)
	if err != nil {
		return err
	}
	if saved == nil {
		return errors.New("release has no recorded provider association; request a new release")
	}
	var expected, actual providerBinding
	if err := json.Unmarshal(*saved, &expected); err != nil {
		return errors.New("release provider association is malformed")
	}
	if err := json.Unmarshal(*current, &actual); err != nil {
		return err
	}
	if expected != actual {
		return errors.New("release provider association changed; request a new release")
	}
	return nil
}

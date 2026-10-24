package integration

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flokkit/apptown/internal/database"
)

type BoundRegistry struct{ Name, URL string }

// RegistryBindings is the bound package registries by format. A listing
// that fails, or a binding whose config does not decode, is an error: a
// mirror left out would send builds directly upstream.
func RegistryBindings(ctx context.Context, db database.Queryer) (map[string]BoundRegistry, error) {
	bindings, err := ForCapability(ctx, db, CapRegistry)
	if err != nil {
		return nil, fmt.Errorf("reading the registry bindings: %w", err)
	}
	out := map[string]BoundRegistry{}
	for _, b := range bindings {
		var cfg struct {
			Format, URL string
		}
		if err := json.Unmarshal([]byte(b.ConfigJSON), &cfg); err != nil {
			return nil, fmt.Errorf("the registry binding %s's configuration cannot be read: %w", b.Name, err)
		}
		if cfg.Format != "" && cfg.URL != "" {
			out[cfg.Format] = BoundRegistry{Name: b.Name, URL: cfg.URL}
		}
	}
	return out, nil
}

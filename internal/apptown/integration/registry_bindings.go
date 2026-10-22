package integration

import (
	"context"
	"encoding/json"
	"log"

	"github.com/flokkit/apptown/internal/database"
)

type BoundRegistry struct{ Name, URL string }

func RegistryBindingsIn(ctx context.Context, db database.Queryer) map[string]BoundRegistry {
	out, _ := RegistryBindings(ctx, db)
	return out
}

// RegistryBindings is RegistryBindingsIn with the failure to read them, for
// a caller that must not take "could not read" as "none".
func RegistryBindings(ctx context.Context, db database.Queryer) (map[string]BoundRegistry, error) {
	bindings, err := ForCapability(ctx, db, CapRegistry)
	if err != nil {
		return nil, err
	}
	out := map[string]BoundRegistry{}
	for _, b := range bindings {
		var cfg struct {
			Format, URL string
		}
		// A binding whose config does not decode is skipped, and builds pull
		// directly: said in the log, not mistaken for a mirror.
		if err := json.Unmarshal([]byte(b.ConfigJSON), &cfg); err != nil {
			log.Printf("registry binding %s: its config could not be read, so it is not used: %v", b.Name, err)
			continue
		}
		if cfg.Format != "" && cfg.URL != "" {
			out[cfg.Format] = BoundRegistry{Name: b.Name, URL: cfg.URL}
		}
	}
	return out, nil
}

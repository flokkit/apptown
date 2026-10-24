package model

import (
	"context"
	"fmt"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/database"
)

const (
	hostedModelCapacity = 8
	selfHostedCapacity  = 1
)

// BindingCapacity reads current Integration configuration, never model-slot
// mappings. It holds the actual record owner, not App or a raw database.
type BindingCapacity struct{ Bindings *integration.Bindings }

// Capacity returns the configured positive concurrency, otherwise one for custom
// endpoints or eight for hosted providers. No binding name, or a name no binding
// has, returns zero, so [Scheduler] uses its configured default. A failed read,
// a binding of another capability or a configuration that does not parse is an
// error: an unknown capacity is never a valid one.
func (s BindingCapacity) Capacity(ctx context.Context, binding string) (int, error) {
	if binding == "" {
		return 0, nil
	}
	b, err := s.Bindings.ByName(ctx, binding)
	if found, err := database.Lookup(err); err != nil || !found {
		return 0, err
	}
	if b.Capability != "model" {
		return 0, fmt.Errorf("binding %q is a %s binding, not a model binding", binding, b.Capability)
	}
	cfg, err := b.ModelConfig()
	if err != nil {
		return 0, err
	}
	if cfg.Concurrency > 0 {
		return cfg.Concurrency, nil
	}
	if cfg.ProviderDef != nil || cfg.BaseURL != "" {
		return selfHostedCapacity, nil
	}
	return hostedModelCapacity, nil
}

// BindingOfPattern names the binding of a resolved provider/model pattern.
func BindingOfPattern(pattern string) string {
	if i := strings.Index(pattern, "/"); i > 0 {
		return pattern[:i]
	}
	return pattern
}

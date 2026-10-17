package model

import (
	"context"
	"strings"

	"flokkit.io/forge/internal/forge/integration"
)

const (
	hostedModelCapacity = 8
	selfHostedCapacity  = 1
)

// BindingCapacity reads current Integration configuration, never model-slot
// mappings. It holds the actual record owner, not App or a raw database.
type BindingCapacity struct{ Bindings *integration.Bindings }

// Capacity returns the configured positive concurrency, otherwise one for custom
// endpoints or eight for hosted providers. An absent, unreadable or invalid model
// binding returns zero, allowing [Scheduler] to use its fallback.
func (s BindingCapacity) Capacity(ctx context.Context, binding string) int {
	if binding == "" {
		return 0
	}
	b, err := s.Bindings.ByName(ctx, binding)
	if err != nil || b.Capability != "model" {
		return 0
	}
	cfg, err := b.ModelConfig()
	if err != nil {
		return 0
	}
	if cfg.Concurrency > 0 {
		return cfg.Concurrency
	}
	if cfg.ProviderDef != nil || cfg.BaseURL != "" {
		return selfHostedCapacity
	}
	return hostedModelCapacity
}

// BindingOfPattern names the binding of a resolved provider/model pattern.
func BindingOfPattern(pattern string) string {
	if i := strings.Index(pattern, "/"); i > 0 {
		return pattern[:i]
	}
	return pattern
}

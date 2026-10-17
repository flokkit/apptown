package inference

import (
	"context"
	"encoding/json"
	"sort"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/integration"
)

// ModelEntry is one selectable model pattern; provider credentials never
// enter this view.
type ModelEntry struct {
	Pattern string `json:"pattern"`
	Binding string `json:"binding"`
	// Source is where the pattern comes from: the binding's default, its
	// curated list, or the provider's own model list (live).
	Source string `json:"source"`
}

// ErrNoEndpoint is a binding whose provider has no model list to ask.
var ErrNoEndpoint = fault.New(fault.Unprocessable, "no discoverable endpoint for this provider")

// Models lists the patterns a model slot may name, binding by binding:
// the default, the curated list, and what the provider lists live, read
// through the same resolution and discovery cache the gateway admits by.
// A binding that does not resolve (a missing credential, say) still lists
// its configured patterns, without live ones.
func (catalog Catalog) Models(ctx context.Context) ([]ModelEntry, error) {
	bindings, err := integration.ForCapability(ctx, catalog.db, integration.CapModel)
	if err != nil {
		return nil, err
	}
	secrets := catalog.secrets.All(ctx)
	seen := map[string]bool{}
	var out []ModelEntry
	add := func(pattern, binding, source string) {
		if pattern == "" || seen[pattern] {
			return
		}
		seen[pattern] = true
		out = append(out, ModelEntry{Pattern: pattern, Binding: binding, Source: source})
	}
	for _, b := range bindings {
		var cfg integration.ModelConfig
		if json.Unmarshal([]byte(b.ConfigJSON), &cfg) != nil {
			continue
		}
		add(cfg.Model, b.Name, "default")
		for _, id := range cfg.Models {
			add(b.Name+"/"+id, b.Name, "curated")
		}
		p, err := resolveInferenceProvider(b.Name, cfg, secrets)
		if err != nil || p == nil || !discoverable(p, cfg) {
			continue
		}
		for _, id := range catalog.discovery.models(p, clock.Or(catalog.Now)()) {
			add(b.Name+"/"+id, b.Name, "live")
		}
	}
	return out, nil
}

// Discover asks a provider for its models before its binding is saved, with
// the configuration as given: resolved by the same rules as a saved binding,
// credential references included. A built-in provider is asked at its own
// address.
func (catalog Catalog) Discover(ctx context.Context, name string, config map[string]any) ([]string, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, integration.ErrInvalidConfig
	}
	var cfg integration.ModelConfig
	if json.Unmarshal(raw, &cfg) != nil {
		return nil, integration.ErrInvalidConfig
	}
	p, err := resolveInferenceProvider(name, cfg, catalog.secrets.All(ctx))
	if err != nil {
		return nil, fault.Wrap(fault.Unprocessable, err)
	}
	if p == nil || p.api != "openai-completions" && p.api != "openai-responses" {
		return nil, ErrNoEndpoint
	}
	ids, err := fetchModels(p)
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

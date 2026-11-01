package inference

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/fault"
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

// UnlistedBinding is a model binding whose models could not all be listed,
// and why: its configuration does not parse or resolve (a missing
// credential, say), or its provider's live list could not be read. Its
// configured patterns are still listed when they could be read.
type UnlistedBinding struct {
	Binding string `json:"binding"`
	Error   string `json:"error"`
}

// errNoEndpoint is a binding whose provider has no model list to ask.
var errNoEndpoint = fault.New(fault.Unprocessable, "no discoverable endpoint for this provider")

// Models lists the patterns a model slot may name, binding by binding:
// the default, the curated list, and what the provider lists live, read
// through the same resolution and discovery cache the gateway admits by.
// A binding whose models could not all be listed is named in unlisted with
// why, so an unread list is never shown as an empty or current one.
func (catalog Catalog) Models(ctx context.Context) (entries []ModelEntry, unlisted []UnlistedBinding, err error) {
	bindings, err := integration.ForCapability(ctx, catalog.db, integration.CapModel)
	if err != nil {
		return nil, nil, err
	}
	secrets, err := catalog.secrets.WithContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	add := func(pattern, binding, source string) {
		if pattern == "" || seen[pattern] {
			return
		}
		seen[pattern] = true
		entries = append(entries, ModelEntry{Pattern: pattern, Binding: binding, Source: source})
	}
	for _, b := range bindings {
		var cfg integration.ModelConfig
		if json.Unmarshal([]byte(b.ConfigJSON), &cfg) != nil {
			unlisted = append(unlisted, UnlistedBinding{Binding: b.Name, Error: "its configuration could not be read"})
			continue
		}
		add(cfg.Model, b.Name, "default")
		for _, id := range cfg.Models {
			add(b.Name+"/"+id, b.Name, "curated")
		}
		p, err := resolveInferenceProvider(b.Name, cfg, secrets)
		if err != nil {
			unlisted = append(unlisted, UnlistedBinding{Binding: b.Name, Error: err.Error()})
			continue
		}
		if p == nil || !discoverable(p, cfg) {
			continue
		}
		ids, err := catalog.discovery.models(ctx, p, clock.Or(catalog.Now)())
		if err != nil {
			unlisted = append(unlisted, UnlistedBinding{Binding: b.Name, Error: err.Error()})
		}
		for _, id := range ids {
			add(b.Name+"/"+id, b.Name, "live")
		}
	}
	return entries, unlisted, nil
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
	secrets, err := catalog.secrets.WithContext(ctx)
	if err != nil {
		return nil, err
	}
	p, err := resolveInferenceProvider(name, cfg, secrets)
	if err != nil {
		return nil, fault.Wrap(fault.Unprocessable, err)
	}
	if p == nil || p.api != "openai-completions" && p.api != "openai-responses" {
		return nil, errNoEndpoint
	}
	ids, err := fetchModels(ctx, p)
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

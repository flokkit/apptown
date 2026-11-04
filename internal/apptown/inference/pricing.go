package inference

import (
	_ "embed"
	"encoding/json"
)

//go:generate node ../../../scripts/generate-model-catalog.mjs

//go:embed model-catalog.json
var modelCatalogJSON []byte

type rates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}
type modelPrice struct {
	rates
	Tiers []struct {
		rates
		InputTokensAbove int64 `json:"inputTokensAbove"`
	} `json:"tiers,omitempty"`
}
type catalogModel struct {
	Cost          modelPrice `json:"cost"`
	ContextWindow int64      `json:"contextWindow"`
}

var builtInCatalog = func() struct {
	PiVersion string                             `json:"piVersion"`
	Providers map[string]map[string]catalogModel `json:"providers"`
} {
	var c struct {
		PiVersion string                             `json:"piVersion"`
		Providers map[string]map[string]catalogModel `json:"providers"`
	}
	if err := json.Unmarshal(modelCatalogJSON, &c); err != nil {
		panic(err)
	}
	return c
}()

func builtinPricing(provider string) bool {
	switch provider {
	case "openai", "anthropic", "openrouter", "google":
		return true
	}
	return false
}

// price uses the requested, admitted model, never an untrusted returned alias.
// Input in OpenAI/Google usage includes cache hits; Anthropic excludes them.
func (p *inferenceProvider) price(id string, u *inferenceUsage) {
	if u.Priced {
		u.PriceSource = "provider-reported"
		return
	}
	u.PriceSource = "none"
	if !u.Observed {
		return
	}
	var cost modelPrice
	if builtinPricing(p.name) {
		cost = builtInCatalog.Providers[p.name][id].Cost
	} else {
		raw, err := json.Marshal(p.models[id]["cost"])
		if err != nil {
			return
		}
		if err := json.Unmarshal(raw, &cost); err != nil {
			return
		}
		if overrides, ok := p.metadata["modelOverrides"].(map[string]any); ok {
			if m, ok := overrides[id].(map[string]any); ok {
				raw, err = json.Marshal(m["cost"])
				if err != nil {
					return
				}
				if err := json.Unmarshal(raw, &cost); err != nil {
					return
				}
			}
		}
	}
	input := u.Input
	if p.api != "anthropic-messages" {
		input = max(0, input-u.CacheRead-u.CacheWrite)
	}
	r := cost.rates
	threshold := int64(-1)
	for _, tier := range cost.Tiers {
		if input+u.CacheRead+u.CacheWrite > tier.InputTokensAbove && tier.InputTokensAbove > threshold {
			r, threshold = tier.rates, tier.InputTokensAbove
		}
	}
	if r.Input+r.Output+r.CacheRead+r.CacheWrite == 0 {
		return
	}
	u.Cost = (float64(input)*r.Input + float64(u.Output)*r.Output + float64(u.CacheRead)*r.CacheRead + float64(u.CacheWrite-u.CacheWrite1h)*r.CacheWrite + float64(u.CacheWrite1h)*r.Input*2) / 1e6
	u.Priced, u.PriceSource = true, "catalog"
}

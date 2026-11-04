package inference

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestCatalogMatchesWorkerPin(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/apptown-agent/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^ARG PI_VERSION=(\S+)`).FindSubmatch(raw)
	if len(pin) != 2 || string(pin[1]) != builtInCatalog.PiVersion {
		t.Fatalf("regenerate model catalog: worker pin %q, artifact %s", pin, builtInCatalog.PiVersion)
	}
}

// Token categories in provider usage overlap. Cache hits are removed from
// OpenAI input, while Anthropic input already excludes cache reads/writes.
func TestCatalogPricesProviderTokenSemantics(t *testing.T) {
	for _, tc := range []struct {
		provider, api, id string
		input             int64
	}{
		{"openai", "openai-responses", "gpt-6.1-terra", 1010},
		{"anthropic", "anthropic-messages", "claude-sonnet-4-5", 800},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			p := &inferenceProvider{name: tc.provider, api: tc.api}
			u := inferenceUsage{Observed: true, Input: tc.input, Output: 50, CacheRead: 200, CacheWrite: 10}
			p.price(tc.id, &u)
			r := builtInCatalog.Providers[tc.provider][tc.id].Cost
			want := (800*r.Input + 50*r.Output + 200*r.CacheRead + 10*r.CacheWrite) / 1e6
			if !u.Priced || u.PriceSource != "catalog" || want == 0 || u.Cost != want {
				t.Fatalf("usage %+v, want %g", u, want)
			}
		})
	}
}

func TestDeclaredPricesAndReportedPrices(t *testing.T) {
	p := &inferenceProvider{name: "private", api: "openai-completions", models: map[string]map[string]any{}}
	for i, raw := range []string{`{"input":2,"output":4,"cacheRead":1,"cacheWrite":3}`, `{"input":0,"output":0}`, `null`, `{"input":2,"output":"invalid"}`} {
		var cost any
		if err := json.Unmarshal([]byte(raw), &cost); err != nil {
			t.Fatal(err)
		}
		p.models["m"] = map[string]any{"cost": cost}
		u := inferenceUsage{Observed: true, Input: 100, Output: 10, CacheRead: 20}
		p.price("m", &u)
		if i == 0 {
			if u.PriceSource != "catalog" || u.Cost != 0.00022 {
				t.Fatalf("declared: %+v", u)
			}
		} else if u.Priced || u.PriceSource != "none" {
			t.Fatalf("placeholder: %+v", u)
		}
	}
	p.name = "openrouter"
	u := inferenceUsage{Observed: true, Priced: true, Cost: 0}
	p.price("unknown", &u)
	if !u.Priced || u.Cost != 0 || u.PriceSource != "provider-reported" {
		t.Fatalf("reported zero: %+v", u)
	}
	u.Cost = 0.005
	p.price("unknown", &u)
	if u.Cost != 0.005 {
		t.Fatal("overwrote provider price")
	}
}

func TestCatalogPricesLongContextAndLongCacheWrites(t *testing.T) {
	p := &inferenceProvider{name: "custom", api: "anthropic-messages", models: map[string]map[string]any{}}
	var definition map[string]any
	if err := json.Unmarshal([]byte(`{"cost":{"input":2,"output":4,"cacheRead":1,"cacheWrite":3,"tiers":[{"inputTokensAbove":100,"input":4,"output":8,"cacheRead":2,"cacheWrite":6}]}}`), &definition); err != nil {
		t.Fatal(err)
	}
	p.models["m"] = definition
	for _, input := range []int64{80, 81} {
		u := inferenceUsage{Observed: true, Input: input, Output: 10, CacheRead: 10, CacheWrite: 10, CacheWrite1h: 5}
		p.price("m", &u)
		want := (float64(input)*2 + 40 + 10 + 15 + 20) / 1e6
		if input == 81 {
			want *= 2
		}
		if u.PriceSource != "catalog" || u.Cost != want {
			t.Fatalf("input %d: %+v want %g", input, u, want)
		}
	}
}

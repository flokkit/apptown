package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/integration"
	"flokkit.io/forge/internal/forge/secret"
)

// Only this server-side definition may contain provider credentials. Never
// serialize it into a workspace, a console, a receipt or an error response.
type inferenceProvider struct {
	name, baseURL, api, key string
	headers                 map[string]string
	models                  map[string]map[string]any
	patterns                []string
	metadata                map[string]any
}

var inferenceProviderName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func (catalog Catalog) providers(ctx context.Context) (map[string]*inferenceProvider, error) {
	bindings, err := integration.ForCapability(ctx, catalog.db, "model")
	if err != nil {
		return nil, err
	}
	secrets := catalog.secrets.All(ctx)
	providers := map[string]*inferenceProvider{}
	for _, b := range bindings {
		name, raw := b.Name, b.ConfigJSON
		var cfg integration.ModelConfig
		if json.Unmarshal([]byte(raw), &cfg) != nil {
			return nil, fmt.Errorf("invalid model binding %q", name)
		}
		p, err := resolveInferenceProvider(name, cfg, secrets)
		if err != nil {
			return nil, err
		}
		// An unconfigured seed is not a usable backend.
		if p != nil {
			catalog.discoverModels(p, cfg)
			providers[name] = p
		}
	}
	return providers, nil
}

func resolveInferenceProvider(name string, cfg integration.ModelConfig, secrets map[string]string) (*inferenceProvider, error) {
	if !inferenceProviderName.MatchString(name) {
		return nil, fmt.Errorf("invalid model binding name %q", name)
	}
	p := &inferenceProvider{name: name, baseURL: cfg.BaseURL, api: cfg.API, models: map[string]map[string]any{}, headers: map[string]string{}, metadata: map[string]any{}}
	def := cfg.ProviderDef
	if s, ok := def["baseUrl"].(string); ok {
		p.baseURL = s
	}
	if s, ok := def["api"].(string); ok {
		p.api = s
	}
	if p.baseURL == "" {
		switch name {
		case "openrouter":
			p.baseURL, p.api = "https://openrouter.ai/api/v1", "openai-completions"
		case "anthropic":
			p.baseURL, p.api = "https://api.anthropic.com", "anthropic-messages"
		case "openai":
			p.baseURL, p.api = "https://api.openai.com/v1", "openai-responses"
		case "google":
			p.baseURL, p.api = "https://generativelanguage.googleapis.com", "google-generative-ai"
		default:
			if cfg.Model == "" && len(cfg.Models) == 0 && len(def) == 0 {
				return nil, nil
			}
			return nil, fmt.Errorf("model binding %q needs an explicit inference base URL and API dialect", name)
		}
	}
	if p.api == "" {
		p.api = "openai-completions"
	}
	switch p.api {
	case "openai-completions", "openai-responses", "anthropic-messages", "google-generative-ai":
	default:
		return nil, fmt.Errorf("model binding %q has an unsupported inference API", name)
	}
	u, err := url.Parse(p.baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("model binding %q needs an HTTP(S) base URL without credentials or query parameters", name)
	}
	p.baseURL = strings.TrimRight(p.baseURL, "/")
	for _, ref := range append(append([]string{}, cfg.APIKeyFrom...), cfg.SecretEnv...) {
		if secrets[ref] != "" {
			p.key = secrets[ref]
			break
		}
	}
	if p.key == "" {
		if s, ok := def["apiKey"].(string); ok {
			if strings.HasPrefix(s, "!") {
				return nil, fmt.Errorf("model binding %q must use a secret reference, not a credential command", name)
			}
			if strings.HasPrefix(s, "$") {
				p.key = secrets[strings.TrimPrefix(s, "$")]
			} else {
				p.key = s
			}
		}
	}
	if (len(cfg.APIKeyFrom)+len(cfg.SecretEnv) > 0) && p.key == "" {
		return nil, fmt.Errorf("model binding %q has no available provider credential", name)
	}
	if hdrs, ok := def["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if s, ok := v.(string); ok {
				if strings.HasPrefix(s, "!") {
					return nil, fmt.Errorf("model binding %q contains a header command", name)
				}
				if strings.HasPrefix(s, "$") {
					s = secrets[strings.TrimPrefix(s, "$")]
				}
				// These are operator-supplied UPSTREAM headers only, never client headers.
				if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Connection") {
					return nil, fmt.Errorf("model binding %q has an unsupported upstream header", name)
				}
				p.headers[k] = s
			}
		}
	}
	addModels := func(v any) error {
		arr, _ := v.([]any)
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("model binding %q contains an invalid model", name)
			}
			id, _ := m["id"].(string)
			if id == "" {
				return fmt.Errorf("model binding %q contains an unnamed model", name)
			}
			if api, ok := m["api"].(string); ok && api != p.api {
				return fmt.Errorf("model binding %q mixes API dialects; split it into separate bindings", name)
			}
			p.models[id] = publicPiModel(m)
		}
		return nil
	}
	if err := addModels(def["models"]); err != nil {
		return nil, err
	}
	if cfg.ModelsJSON != "" && def["models"] == nil {
		var v any
		if json.Unmarshal([]byte(cfg.ModelsJSON), &v) != nil {
			return nil, fmt.Errorf("model binding %q has invalid model JSON", name)
		}
		if err := addModels(v); err != nil {
			return nil, err
		}
	}
	// A curated list is a real admission limit. Without one, declared models
	// and the configured default pattern are allowed, never every provider model.
	if len(cfg.Models) > 0 {
		p.patterns = append(p.patterns, cfg.Models...)
	} else {
		for id := range p.models {
			p.patterns = append(p.patterns, id)
		}
		if cfg.Model != "" {
			p.patterns = append(p.patterns, strings.TrimPrefix(cfg.Model, name+"/"))
		}
	}
	for _, k := range []string{"compat"} {
		if v, ok := def[k]; ok {
			p.metadata[k] = v
		}
	}
	if overrides, ok := def["modelOverrides"].(map[string]any); ok {
		clean := map[string]any{}
		for id, v := range overrides {
			if m, ok := v.(map[string]any); ok {
				clean[id] = publicPiModel(m)
			}
		}
		p.metadata["modelOverrides"] = clean
	}
	return p, nil
}

func publicPiModel(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"id", "name", "reasoning", "thinkingLevelMap", "input", "contextWindow", "maxTokens", "cost", "compat", "samplingParams"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func (p *inferenceProvider) allows(model string) bool {
	if model == "" || strings.ContainsAny(model, "\r\n?#") {
		return false
	}
	for _, pattern := range p.patterns {
		// Pi's ~ selector is fuzzy, not an authorization wildcard. A concrete
		// curated/declared ID is required for it. Plain * patterns are supported.
		if strings.HasPrefix(pattern, "~") {
			continue
		}
		re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
		if ok, _ := regexp.MatchString(re, model); ok {
			return true
		}
	}
	return false
}

type Catalog struct {
	db        *database.DB
	secrets   *secret.Service
	discovery *inferenceDiscovery
	// Now is the forge's clock, which ages the discovery cache; nil is the
	// real one.
	Now func() time.Time
}

// NewCatalog constructs the private provider catalog and its public discovery cache.
func NewCatalog(db *database.DB, secrets *secret.Service) *Catalog {
	return &Catalog{db: db, secrets: secrets, discovery: &inferenceDiscovery{}}
}

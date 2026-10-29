package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/trust"
)

// AnthropicBase is Anthropic's API address, as a built-in anthropic binding
// resolves it.
const AnthropicBase = "https://api.anthropic.com"

// listedModel is one entry of a provider's model list: its id, and for an
// OpenRouter alias the model it names.
type listedModel struct {
	ID          string `json:"id"`
	AliasTarget struct {
		Slug string `json:"slug"`
	} `json:"alias_target"`
}

// listModels is the one way AppTown asks a provider for its models: a GET
// of url, with authorize setting the binding's headers and credential
// (nil for a public list), bounded by ctx and ten seconds, on a pool of its
// own with no proxy and no redirects (a key goes to url only), at most
// 8 MiB, 200 only. name names the provider in errors, which never quote
// the answer.
func listModels(ctx context.Context, name, url string, authorize func(http.Header)) ([]listedModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if authorize != nil {
		authorize(req.Header)
	}
	transport := trust.Transport(trust.Inference)
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s could not be asked for its models", name)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered HTTP %d when asked for its models", name, resp.StatusCode)
	}
	var list struct {
		Data []listedModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("%s's model list could not be read", name)
	}
	return list.Data, nil
}

// AnthropicNewest is the newest model in Anthropic's list whose id starts
// with family ("claude-sonnet-"), asked at base+"/v1/models" with key.
// Anthropic lists the most recently released models first, so the first
// match is the newest; no version is parsed. A list that cannot be read, or
// names no such model, is an error.
func AnthropicNewest(ctx context.Context, base, key, family string) (string, error) {
	p := &inferenceProvider{name: "anthropic", baseURL: strings.TrimRight(base, "/"), api: "anthropic-messages", key: key, headers: map[string]string{}}
	list, err := listModels(ctx, "Anthropic", p.baseURL+"/v1/models", func(h http.Header) {
		p.authorize(h)
		h.Set("Anthropic-Version", "2023-06-01")
	})
	if err != nil {
		return "", err
	}
	for _, m := range list {
		if strings.HasPrefix(m.ID, family) {
			return m.ID, nil
		}
	}
	return "", fmt.Errorf("Anthropic lists no %s model", strings.TrimSuffix(family, "-"))
}

// OpenRouterAlias is the model a "~" alias names in OpenRouter's public
// model list at base+"/models" (for "~anthropic/claude-sonnet-latest",
// today "anthropic/claude-sonnet-5.5"). The list needs no key. An alias
// that is missing, names no target, or names another alias is an error.
func OpenRouterAlias(ctx context.Context, base, alias string) (string, error) {
	list, err := listModels(ctx, "OpenRouter", strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return "", err
	}
	for _, m := range list {
		if m.ID != alias {
			continue
		}
		if m.AliasTarget.Slug == "" || strings.HasPrefix(m.AliasTarget.Slug, "~") {
			return "", fmt.Errorf("OpenRouter's %s names no model", alias)
		}
		return m.AliasTarget.Slug, nil
	}
	return "", fmt.Errorf("OpenRouter lists no %s", alias)
}

// fetchModels lists the model IDs an OpenAI-style provider offers at
// /models, with the binding's own headers and credential.
func fetchModels(ctx context.Context, p *inferenceProvider) ([]string, error) {
	list, err := listModels(ctx, "the "+p.name+" provider", p.baseURL+"/models", p.authorize)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range list {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

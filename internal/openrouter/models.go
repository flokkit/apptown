package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/trust"
)

// modelsHTTP reads the public model list on a pool of its own.
var modelsHTTP = &http.Client{Transport: trust.Lazy(trust.Inference)}

// AliasTarget is the model a "~" alias names in OpenRouter's public model
// list at base+"/models" (for "~anthropic/claude-sonnet-latest", today
// "anthropic/claude-sonnet-5.5"). The list needs no key. An alias that is
// missing, names no target, or names another alias is an error.
func AliasTarget(ctx context.Context, base, alias string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return "", err
	}
	resp, err := modelsHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("reading OpenRouter's model list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("reading OpenRouter's model list: HTTP %d", resp.StatusCode)
	}
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			AliasTarget struct {
				Slug string `json:"slug"`
			} `json:"alias_target"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return "", fmt.Errorf("reading OpenRouter's model list: %w", err)
	}
	for _, m := range list.Data {
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

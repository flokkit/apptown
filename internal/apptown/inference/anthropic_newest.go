package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AnthropicBase is Anthropic's API address, as a built-in anthropic binding
// resolves it.
const AnthropicBase = "https://api.anthropic.com"

// AnthropicNewest is the newest model in Anthropic's list whose id starts
// with family ("claude-sonnet-"), asked at base+"/v1/models" with key.
// Anthropic lists the most recently released models first, so the first
// match is the newest; no version is parsed. A list that cannot be read, or
// names no such model, is an error.
func AnthropicNewest(ctx context.Context, base, key, family string) (string, error) {
	p := &inferenceProvider{name: "anthropic", baseURL: strings.TrimRight(base, "/"), api: "anthropic-messages", key: key, headers: map[string]string{}}
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/v1/models", nil)
	if err != nil {
		return "", err
	}
	p.authorize(req.Header)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	// Its own pool, no proxy and no redirects: the key goes to base only.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Anthropic could not be asked for its models")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Anthropic answered HTTP %d when asked for its models", resp.StatusCode)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return "", fmt.Errorf("Anthropic's model list could not be read")
	}
	for _, m := range list.Data {
		if strings.HasPrefix(m.ID, family) {
			return m.ID, nil
		}
	}
	return "", fmt.Errorf("Anthropic lists no %s model", strings.TrimSuffix(family, "-"))
}

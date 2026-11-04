package inference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Listing uses the binding's dialect even when it permits other inference
// dialects. Both saved discovery and a direct probe preserve its headers.
func TestModelListingAuthorizesWithTheBindingsDialect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		api, header, credential string
	}{
		{"openai-completions", "Authorization", "Bearer provider-key"},
		{"openai-responses", "Authorization", "Bearer provider-key"},
		{"anthropic-messages", "X-Api-Key", "provider-key"},
		{"google-generative-ai", "X-Goog-Api-Key", "provider-key"},
	} {
		t.Run(tc.api, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" || r.Header.Get(tc.header) != tc.credential || r.Header.Get("X-Binding") != "listing" {
					t.Errorf("unexpected model listing: %s, headers %v", r.URL.Path, r.Header)
				}
				for _, header := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
					if header != tc.header && r.Header.Get(header) != "" {
						t.Errorf("credential sent in another dialect's header: %s", header)
					}
				}
				_, _ = w.Write([]byte(`{"data":[{"id":"listed"}]}`))
			}))
			defer server.Close()
			p := &inferenceProvider{name: "custom", baseURL: server.URL, api: tc.api, key: "provider-key", headers: map[string]string{"X-Binding": "listing"}}
			var discovery inferenceDiscovery
			for _, list := range []func(context.Context, *inferenceProvider) ([]string, error){
				fetchModels,
				func(ctx context.Context, p *inferenceProvider) ([]string, error) {
					return discovery.models(ctx, p, time.Now())
				},
			} {
				ids, err := list(t.Context(), p)
				if err != nil || len(ids) != 1 || ids[0] != "listed" {
					t.Fatalf("model listing = %v, %v", ids, err)
				}
			}
		})
	}
}

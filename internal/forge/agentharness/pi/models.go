package pi

import "encoding/json"

// Provider is the public input to Pi's configuration renderer. Inference
// selects allowed models and supplies proxy URLs. There are no fields for private
// upstream credentials; inference checks rendered metadata before releasing it.
type Provider struct {
	Name, URL, API string
	Metadata       map[string]any
	Models         []map[string]any
}

func ModelsJSON(ps []Provider) ([]byte, error) {
	providers := map[string]any{}
	for _, p := range ps {
		def := map[string]any{"baseUrl": p.URL, "api": p.API, "apiKey": "$FORGE_SESSION_TOKEN", "authHeader": true}
		for k, v := range p.Metadata {
			def[k] = v
		}
		if len(p.Models) > 0 {
			def["models"] = p.Models
		}
		providers[p.Name] = def
	}
	return json.MarshalIndent(map[string]any{"providers": providers}, "", "  ")
}

package integration

import (
	"encoding/json"
	"strings"
)

// Config reads are available to runtime credentials too. Inline legacy keys
// and custom authentication headers must not escape through that second door.
func RedactModelCredentials(m map[string]any) {
	for k, v := range m {
		switch strings.ToLower(k) {
		case "apikey", "authorization":
			m[k] = "••• (inline — migrate to a secret reference)"
		case "headers":
			if headers, ok := v.(map[string]any); ok {
				for h := range headers {
					headers[h] = "••• (server-side header)"
				}
			}
		case "modelsjson":
			if raw, ok := v.(string); ok {
				var models []any
				if json.Unmarshal([]byte(raw), &models) == nil {
					for _, model := range models {
						if obj, ok := model.(map[string]any); ok {
							RedactModelCredentials(obj)
						}
					}
					if clean, err := json.Marshal(models); err == nil {
						m[k] = string(clean)
					}
				}
			}
		default:
			switch x := v.(type) {
			case map[string]any:
				RedactModelCredentials(x)
			case []any:
				for _, item := range x {
					if obj, ok := item.(map[string]any); ok {
						RedactModelCredentials(obj)
					}
				}
			}
		}
	}
}

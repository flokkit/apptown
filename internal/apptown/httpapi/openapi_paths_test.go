package httpapi

import (
	"strings"
	"testing"
)

// OpenAPI 3.1 section 4.8.8.1 forbids two paths that differ only in
// template parameter names, even when they describe different methods.
func TestOpenAPIPathsHaveUniqueTemplates(t *testing.T) {
	t.Parallel()
	paths := shippedDescription(t)["paths"].(map[string]any)
	seen := map[string]string{}
	for path := range paths {
		parts := strings.Split(path, "/")
		for i, part := range parts {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				parts[i] = "{}"
			}
		}
		key := strings.Join(parts, "/")
		if other, exists := seen[key]; exists {
			t.Errorf("identical OpenAPI path templates: %s and %s", other, path)
		}
		seen[key] = path
	}
}

package apptown

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/verification"
)

func attachScanner(t *testing.T, a *App, name string, cfg map[string]any) {
	t.Helper()
	// A fixture scanner at a scan point applies to the repository unless it
	// says otherwise (an image scanner then to the public part's image).
	if manual, _ := cfg["manual"].(bool); !manual {
		if _, ok := cfg["appliesTo"]; !ok {
			cfg["appliesTo"] = []string{"repository"}
		}
	}
	if _, err := a.Integrations.Bindings.Upsert(t.Context(), name, verification.ScannerCapability, "tool", mustJSON(cfg), true); err != nil {
		t.Fatal(err)
	}
}

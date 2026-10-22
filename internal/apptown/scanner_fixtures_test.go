package apptown

import (
	"testing"

	"github.com/flokkit/apptown/internal/apptown/verification"
)

func attachScanner(t *testing.T, a *App, name string, cfg map[string]any) {
	t.Helper()
	if _, err := a.Integrations.Bindings.Upsert(t.Context(), name, verification.ScannerCapability, "tool", mustJSON(cfg), true); err != nil {
		t.Fatal(err)
	}
}

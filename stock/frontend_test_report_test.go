package stock

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFrontendStarterWritesJUnitReport(t *testing.T) {
	t.Parallel()
	b, err := FS.ReadFile("seeds/frontend-only/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct{ Scripts map[string]string }
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatal(err)
	}
	want := `vitest run --reporter=default --reporter=junit --outputFile.junit="${APPTOWN_TEST_REPORT:-/dev/null}"`
	if pkg.Scripts["test"] != want {
		t.Fatalf("starter test script = %q, want %q", pkg.Scripts["test"], want)
	}
	guidance, err := FS.ReadFile("guidance/frontend/vite-react.md")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(guidance), string(encoded)) {
		t.Fatal("frontend guidance does not declare the starter test script")
	}
}

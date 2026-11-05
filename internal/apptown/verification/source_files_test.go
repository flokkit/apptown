package verification

import (
	"slices"
	"testing"
)

// Exactly the six broad code rubrics opt into source discovery. Their context
// stays explicit; checks about particular documents keep their authored lists.
func TestStockSourceEvidenceSelectors(t *testing.T) {
	t.Parallel()
	want := map[string][]string{
		"state-behind-needs":         {".apptown/app.yaml", "go.mod", "requirements*.txt", "pyproject.toml", "package.json", "Gemfile", "**/settings.py", "config/database.yml"},
		"no-network-code":            {"**/*.html"},
		"touch-and-keys":             {"**/*.html", "**/*.css"},
		"plays-in-browser":           {"project.godot", "export_presets.cfg", ".apptown/app.yaml"},
		"readable-gdscript":          nil,
		"admin-exposed-deliberately": {"Dockerfile*", "compose*.y*ml", "docker-compose*.y*ml", "app.yaml", "Procfile", "secretspec.toml"},
	}
	specific := map[string][]string{
		"runtime-contract-present": nil, "runtime-applies-os-updates": nil, "toolchain-runtime-drift": nil, "readme-plain-tone": nil,
	}
	found := 0
	for _, sc := range shippedChecks(t) {
		if files, ok := want[sc.Check.Name]; ok {
			found++
			if !sc.Check.SourceFiles || !slices.Equal(sc.Check.Files, files) {
				t.Errorf("%s: source=%v, context=%v; want %v", sc.Check.Name, sc.Check.SourceFiles, sc.Check.Files, files)
			}
		} else if sc.Check.SourceFiles {
			t.Errorf("unexpected source selector: %s", sc.Check.Name)
		}
		if _, ok := specific[sc.Check.Name]; ok {
			if sc.Check.SourceFiles || len(sc.Check.Files) == 0 {
				t.Errorf("specific evidence lost: %+v", sc)
			}
			delete(specific, sc.Check.Name)
		}
	}
	if found != 6 || len(specific) != 0 {
		t.Fatalf("source checks=%d; missing specific checks=%v", found, specific)
	}
}

package verification

import (
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/check"
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

// Resolved contract parts reach the stock collector, including when only one
// of a mixed app's parts matches the check. The old extension list misses Java,
// PHP and TSX and would include the unrelated sibling Go part.
func TestStockStateRubricCollectsApplicableSource(t *testing.T) {
	t.Parallel()
	var c check.Declared
	for _, sc := range shippedChecks(t) {
		if sc.Check.Name == "state-behind-needs" {
			c = sc.Declared()
		}
	}
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		".apptown/app.yaml": "app:\n  backend:\n    build: server\n    public: true\n    tools: [java]\n  ui:\n    static: ui\n    tools: [node]\n",
		"server/Main.java":  "public class Main {}",
		"server/index.php":  "<?php echo 'hello'; ?>",
		"server/ui/App.tsx": "export const App = () => <div>Hello</div>;",
		"ui/other.go":       "package other",
	})
	src := CheckSource(dir)
	it := check.TreeItems([]check.Declared{c}, src)[0]
	want := []string{".apptown/app.yaml", "server/Main.java", "server/index.php", "server/ui/App.tsx"}
	if it.Unreadable != "" || !slices.Equal(it.Files, want) {
		t.Fatalf("stock evidence: %+v; want %v (context: %+v)", it, want, src)
	}
}

// Budget detail counts classified source and context before both caps.
func TestSourceRubricDetailReportsBudgetOmissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    public: true\n    tools: [java]\n"}
	for _, name := range []string{"a.java", "b.java", "c.java", "d.java", "e.java", "f.java"} {
		files[name] = "class App {}\n" + strings.Repeat(" ", 20*1024)
	}
	writeTree(t, dir, files)
	c := check.Declared{Declaration: check.Declaration{Name: "source", AppliesTo: []string{"part"}, Evaluator: "llm", Scope: "tree", SourceFiles: true}}
	items := check.TreeItems([]check.Declared{c, c, c, c, c, c}, CheckSource(dir))
	for _, i := range []int{0, 5} {
		detail := llmVerdictDetail(check.Verdict{Verdict: "unclear"}, items[i], &llmMetadata{Served: "model"})
		want := "judged on 3 of 6 matching files"
		if i == 5 {
			want = "judged on 0 of 6 matching files"
		}
		if !strings.Contains(detail, want) {
			t.Fatalf("detail %q lacks %q", detail, want)
		}
	}
}

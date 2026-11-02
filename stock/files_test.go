package stock

import (
	"bytes"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/stockupdate"
	"github.com/flokkit/apptown/internal/stockupdate/stockbuild"
	"gopkg.in/yaml.v3"
)

// The update file a release ships carries exactly the stock the tests read:
// every file FS embeds (the tool versions list aside, its own update), with
// the same bytes, built from the stock folder as scripts/stock-files.go
// builds it. Its header keeps what update.yaml says and gains the build's
// name and time. So the tests' stock and the shipped stock cannot drift.
//
// Kills: an embed pattern or a file-selection rule that the other lacks
// (dotfiles in seeds, a new folder), or a builder that drops or rewrites a file.
func TestTheShippedWaysOfWorkingAreTheEmbeddedStock(t *testing.T) {
	t.Parallel()
	payload, err := WaysOfWorking(os.DirFS("."))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	b, err := stockbuild.Build(stockupdate.WaysOfWorking, payload, "v1.0.0", at)
	if err != nil {
		t.Fatal(err)
	}
	u, err := stockupdate.Parse(stockupdate.WaysOfWorking, b)
	if err != nil {
		t.Fatal(err)
	}
	embedded := files(t, FS)
	delete(embedded, ToolVersionsFile)
	shipped := files(t, u.Content)
	if got, want := keys(shipped), keys(embedded); !slices.Equal(got, want) {
		t.Fatalf("the shipped update holds\n%v\nthe embedded stock\n%v", got, want)
	}
	for p, want := range embedded {
		if p == "update.yaml" {
			continue
		}
		if !bytes.Equal(shipped[p], want) {
			t.Errorf("%s differs from the embedded copy", p)
		}
	}
	var src, got map[string]any
	if err := yaml.Unmarshal(embedded["update.yaml"], &src); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(shipped["update.yaml"], &got); err != nil {
		t.Fatal(err)
	}
	for k, v := range src {
		if got[k] != v {
			t.Errorf("update.yaml %s: %v, want %v", k, got[k], v)
		}
	}
	if u.Header.Name != "v1.0.0" || !u.Header.Published.Equal(at) {
		t.Errorf("stamped header %+v", u.Header)
	}
}

// The tool versions update carries stock/tool-versions.yaml, stamped.
func TestTheShippedToolVersionsAreTheEmbeddedList(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(ToolVersionsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, ToolVersions) {
		t.Fatal("ToolVersions is not stock/tool-versions.yaml")
	}
	b, err := stockbuild.Build(stockupdate.ToolVersions, map[string][]byte{ToolVersionsFile: raw}, "v1.0.0", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	u, err := stockupdate.Parse(stockupdate.ToolVersions, b)
	if err != nil {
		t.Fatal(err)
	}
	var src, got map[string]any
	shipped, err := fs.ReadFile(u.Content, ToolVersionsFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &src); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(shipped, &got); err != nil {
		t.Fatal(err)
	}
	gotTools, _ := yaml.Marshal(got["tools"])
	srcTools, _ := yaml.Marshal(src["tools"])
	if !bytes.Equal(gotTools, srcTools) || got["name"] != "v1.0.0" {
		t.Fatalf("the shipped list is not the stock one, stamped:\n%s", shipped)
	}
}

func files(t *testing.T, fsys fs.FS) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		out[p] = b
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Stock workflow model requests and shipped model guides use current slot names.
func TestStockUsesCurrentModelNames(t *testing.T) {
	t.Parallel()
	for path, content := range files(t, os.DirFS(".")) {
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			var root yaml.Node
			if err := yaml.Unmarshal(content, &root); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			var check func(*yaml.Node)
			check = func(n *yaml.Node) {
				if n.Kind == yaml.MappingNode {
					for i := 0; i < len(n.Content); i += 2 {
						key, value := n.Content[i], n.Content[i+1]
						if key.Value == "model" && (value.Value == "default" || value.Value == "primary-fast") {
							t.Errorf("%s requests retired model %s", path, value.Value)
						}
					}
				}
				for _, child := range n.Content {
					check(child)
				}
			}
			check(&root)
		}
		if strings.HasPrefix(path, "docs/") && strings.HasSuffix(path, ".md") {
			modelGuide := strings.Contains(path, "model-pi.") || path == "docs/guides/architecture.md"
			if path == "docs/guides/glossary.md" {
				// Deployment profiles still legitimately name their base default.
				_, modelTerms, ok := strings.Cut(string(content), "**Model profile**")
				if !ok {
					t.Fatal("glossary has no model profile entry")
				}
				modelTerms, _, _ = strings.Cut(modelTerms, "**System binding**")
				if strings.Contains(modelTerms, "`default`") {
					t.Errorf("%s names a retired model choice", path)
				}
			}
			if bytes.Contains(content, []byte("primary-fast")) || (modelGuide && bytes.Contains(content, []byte("`default`"))) {
				t.Errorf("%s names a retired model choice", path)
			}
		}
	}
}

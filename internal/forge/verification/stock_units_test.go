package verification

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/stock"
)

// contractSize finds a size written as a contract writes it: `memory: 256Mi`,
// `cpu: 500m`, in YAML or inline in prose.
var contractSize = regexp.MustCompile(`\b(memory|cpu):\s*["']?([0-9][0-9.]*\s*[A-Za-z]*)`)

// What stock content shows an agent or a person to write in .apptown/app.yaml
// is copied into a version 2 contract, which refuses bare M and G. Every
// size the shipped text and seed contracts show must read as version 2 reads
// it. The packs' and dependencies' own YAML files are not app.yaml and keep
// their own reader, so only text and seed contracts are judged.
func TestStockShowsContractSizesInVersion2Units(t *testing.T) {
	t.Parallel()
	seen := 0
	err := fs.WalkDir(stock.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if path.Ext(p) != ".md" && !strings.HasSuffix(p, "/.apptown/app.yaml") {
			return nil
		}
		raw, err := fs.ReadFile(stock.FS, p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, m := range contractSize.FindAllStringSubmatch(line, -1) {
				seen++
				var err error
				if m[1] == "memory" {
					_, err = appcontract.ParseMemory(m[2])
				} else {
					_, err = appcontract.ParseCPU(m[2])
				}
				if err != nil {
					t.Errorf("%s:%d: %s: %v", p, i+1, strings.TrimSpace(line), err)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("no contract size found in stock content; the check has nothing to judge")
	}
}

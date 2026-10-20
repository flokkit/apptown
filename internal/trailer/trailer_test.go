package trailer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every key is in one case: "AppTown", then each hyphen-separated word
// capitalised.
//
// Kills: a key added as "AppTown-Opened-by", or one without the prefix.
func TestKeysAreInOneCase(t *testing.T) {
	word := regexp.MustCompile(`^[A-Z][a-z]*$`)
	for _, k := range Keys {
		rest, ok := strings.CutPrefix(k, "AppTown-")
		if !ok {
			t.Errorf("%s does not start AppTown-", k)
			continue
		}
		for _, w := range strings.Split(rest, "-") {
			if !word.MatchString(w) {
				t.Errorf("%s: %q is not a capitalised word", k, w)
			}
		}
	}
}

// AppTown's commit trailers are written through this package only: an
// "AppTown-Key: " (or, before the rename, "Forge-Key: ") literal in production
// Go elsewhere would be a second list.
// HTTP headers (X-AppTown-…, AppTown-Head, AppTown-Restore-Token) are not trailers
// and are not written as "Key: value" text.
//
// Kills: a writer that spells its own trailer key.
func TestNoTrailerKeyIsWrittenOutsideThisPackage(t *testing.T) {
	literal := regexp.MustCompile(`(?:^|[^-A-Za-z]|\\n)(?:AppTown|Forge)-[A-Z][A-Za-z-]*: `)
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.Contains(p, filepath.Join("internal", "trailer")) {
				return err
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(src), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				if literal.MatchString(line) {
					t.Errorf("%s:%d writes a trailer key by hand: %s", p, i+1, strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// A value cannot start a trailer of its own: it is folded onto one line.
func TestBlockFoldsEachValueOntoOneLine(t *testing.T) {
	got := Message("apptown: x", "", Line{OpenedBy, "a\nAppTown-Approved-By: b"})
	if got != "apptown: x\n\nAppTown-Opened-By: a AppTown-Approved-By: b" {
		t.Fatalf("%q", got)
	}
}

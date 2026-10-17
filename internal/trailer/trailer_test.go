package trailer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every key is in one case: each hyphen-separated word capitalised.
//
// Kills: a key added as "Forge-Opened-by".
func TestKeysAreInOneCase(t *testing.T) {
	word := regexp.MustCompile(`^[A-Z][a-z]*$`)
	for _, k := range Keys {
		for _, w := range strings.Split(k, "-") {
			if !word.MatchString(w) {
				t.Errorf("%s: %q is not a capitalised word", k, w)
			}
		}
	}
}

// Forge's commit trailers are written through this package only: a
// "Forge-Key: " literal in production Go elsewhere would be a second list.
// HTTP headers (X-Forge-…, Forge-Head, Forge-Restore-Token) are not trailers
// and are not written as "Key: value" text.
//
// Kills: a writer that spells its own trailer key.
func TestNoTrailerKeyIsWrittenOutsideThisPackage(t *testing.T) {
	literal := regexp.MustCompile(`(?:^|[^-A-Za-z]|\\n)Forge-[A-Z][A-Za-z-]*: `)
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
	got := Message("forge: x", "", Line{OpenedBy, "a\nForge-Approved-By: b"})
	if got != "forge: x\n\nForge-Opened-By: a Forge-Approved-By: b" {
		t.Fatalf("%q", got)
	}
}

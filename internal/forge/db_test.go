package forge

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/strs"
)

var databaseSuite = dbtest.NewSuite(OpenURL)

func testDB(t *testing.T) *database.DB { t.Helper(); return databaseSuite.New(t) }
func TestMain(m *testing.M)            { os.Exit(databaseSuite.Run(m)) }

// Errors that mean something: no read may
// discard its outcome, no write may be log-and-continue under the old
// name. Sorted reads use lookup or readOK; sorted writes use exec,
// stateWrite or logWrite.
func TestNoSilentReadsOrWrites(t *testing.T) {
	t.Parallel()
	bareRead := regexp.MustCompile(`(?m)^\s*_ = [A-Za-z.]*\.QueryRow(?:Context)?\(`)
	ignoredScan := regexp.MustCompile(`(?m)^\s*_ = [A-Za-z]+\.Scan\(`)
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		if m := bareRead.FindString(src); m != "" {
			t.Errorf("%s: read discards its outcome: %s", path, strings.TrimSpace(m))
		}
		if m := ignoredScan.FindString(src); m != "" {
			t.Errorf("%s: ignored Scan: %s", path, strings.TrimSpace(m))
		}
		if strings.Contains(src, "a.bgWrite(") {
			t.Errorf("%s: bgWrite is gone; choose checked writes or explicit failure logging", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// mustJSON encodes a fixture; a fixture that cannot be encoded is a broken test.
func mustJSON(v any) string {
	text, err := strs.JSON(v)
	if err != nil {
		panic(err)
	}
	return text
}

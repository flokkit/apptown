// Package stockupdatetest writes the update files a release ships, from the
// repository's stock/, for tests that start apptownd or apptown-worker as
// executables: their production loader reads them as it would a release's.
// The binaries never import it (make check-stock-embed).
package stockupdatetest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/stockupdate"
	"github.com/flokkit/apptown/internal/stockupdate/stockbuild"
	"github.com/flokkit/apptown/stock"
)

// Name is the name the files are stamped with: a maintainer's build, whose
// works-with range is not judged.
const Name = "dev-test"

// Dir writes both update files into a new folder and answers it.
func Dir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	Write(t, dir)
	return dir
}

// Write writes both update files into dir.
func Write(t testing.TB, dir string) {
	t.Helper()
	ways, err := stock.WaysOfWorking(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for series, payload := range map[stockupdate.Series]map[string][]byte{
		stockupdate.WaysOfWorking: ways,
		stockupdate.ToolVersions:  {stock.ToolVersionsFile: stock.ToolVersions},
	} {
		b, err := stockbuild.Build(series, payload, Name, at)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stockbuild.FileName(series, Name)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

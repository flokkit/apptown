package dependency

import (
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"

	"flokkit.io/forge/stock"
)

// defaultVersions1x is the version a need declared without one gets, in
// every 1.x release (the compatibility guide promises it). A deployment's
// data is made by its server; moving a default would send an app that wrote
// `postgres: {}` to a newer server on its old data at its next release.
// This table changes only with a new major version of AppTown.
var defaultVersions1x = map[string]string{
	"postgres": "16",
	"mariadb":  "11.4.13",
	"redis":    "7",
}

// Every stock kind that takes a version defaults to the version this table
// fixes for 1.x, and every entry is a stock kind: editing a stock file's
// default fails here, not in an app's next release.
func TestStockNeedDefaultVersionsAreFixedFor1x(t *testing.T) {
	t.Parallel()
	files, err := fs.Glob(stock.FS, "dependencies/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("stock kinds: %v %v", files, err)
	}
	versioned := map[string]string{}
	for _, f := range files {
		k := stockKind(t, strings.TrimSuffix(strings.TrimPrefix(f, "dependencies/"), ".yaml"))
		if v, ok := k.Defaults["version"]; ok {
			versioned[k.Capability] = v
		}
	}
	for _, name := range slices.Sorted(maps.Keys(versioned)) {
		if want, ok := defaultVersions1x[name]; !ok {
			t.Errorf("stock need %s defaults to version %s, which the 1.x table does not fix", name, versioned[name])
		} else if versioned[name] != want {
			t.Errorf("stock need %s defaults to version %s; 1.x fixes it at %s", name, versioned[name], want)
		}
	}
	for name := range defaultVersions1x {
		if _, ok := versioned[name]; !ok {
			t.Errorf("the 1.x table fixes %s's default version, but no stock need %s defaults one", name, name)
		}
	}
}

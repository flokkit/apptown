// Package stock is the stock AppTown ships: workflows, guidance, policies,
// packs, the change catalog, starters, toolchain files, scanner and need
// definitions, and the tool versions list. The build writes it into the
// update files a release carries (share/apptown/updates/, scripts/stock-files.go),
// and AppTown reads it from there at start. This package's embedded copy is
// for tests, which may read stock directly: the binaries never import it
// (make check-stock-embed). AppTown's own documentation is stock/docs.
package stock

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/flokkit/apptown/internal/runtimeimages"
)

// FS is the stock, as tests read it.
//
// all:seeds — plain "seeds" silently drops dotfiles, which cost us the
// .apptown/app.yaml runtime contracts seeds are supposed to carry.
//
//go:embed *.yaml toolchains/*.toml dependencies/*.yaml all:guidance all:seeds
var FS embed.FS

// ToolVersions is stock/tool-versions.yaml, as tests read it.
//
//go:embed tool-versions.yaml
var ToolVersions []byte

// ToolVersionsFile is the tool versions list's path in stock.
const ToolVersionsFile = "tool-versions.yaml"

// WaysOfWorking is the content of the ways-of-working update built from a
// stock directory: every file the embedded copy has, the tool versions list
// aside (it is its own update). It is chosen by the same rules as FS's
// patterns, so the update and the tests' copy hold the same files.
func WaysOfWorking(fsys fs.FS) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !inWaysOfWorking(p) {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out[p] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("stock: %w", err)
	}
	return out, nil
}

func inWaysOfWorking(p string) bool {
	dir, file := path.Split(p)
	switch {
	case dir == "":
		return strings.HasSuffix(file, ".yaml") && !strings.HasPrefix(file, ".") && p != ToolVersionsFile
	case dir == "toolchains/":
		return strings.HasSuffix(file, ".toml") && !strings.HasPrefix(file, ".")
	case dir == "dependencies/":
		return strings.HasSuffix(file, ".yaml") && !strings.HasPrefix(file, ".")
	}
	return strings.HasPrefix(p, "guidance/") || strings.HasPrefix(p, "seeds/")
}

// Tools is ToolVersions read, for tests that hold packs to its lines.
func Tools() *runtimeimages.List {
	l, err := runtimeimages.Parse(ToolVersions)
	if err != nil {
		panic(err)
	}
	return l
}

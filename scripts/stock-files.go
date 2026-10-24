//go:build ignore

// stock-files writes the update files a release ships with — the
// ways-of-working update and the tool versions update — from stock/ into a
// folder (share/apptown/updates beside bin/; internal/stockupdate). Run it
// from the repository's root with the same -ldflags as the binaries, so the
// updates are named with the version the binaries report:
//
//	go run -ldflags "$ld" scripts/stock-files.go -o share/apptown/updates
//
// The folder's earlier update files are replaced.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/stockupdate"
	"github.com/flokkit/apptown/internal/stockupdate/stockbuild"
	"github.com/flokkit/apptown/internal/version"
	"github.com/flokkit/apptown/stock"
)

func main() {
	out := flag.String("o", "share/apptown/updates", "the folder to write the update files into")
	src := flag.String("stock", "stock", "the stock folder")
	flag.Parse()
	name, published := version.String(), time.Now().UTC().Truncate(time.Second)
	ways, err := stock.WaysOfWorking(os.DirFS(*src))
	if err != nil {
		log.Fatal(err)
	}
	tools, err := os.ReadFile(filepath.Join(*src, stock.ToolVersionsFile))
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	old, err := filepath.Glob(filepath.Join(*out, "*"+stockupdate.FileSuffix))
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			log.Fatal(err)
		}
	}
	for _, u := range []struct {
		series  stockupdate.Series
		payload map[string][]byte
	}{
		{stockupdate.WaysOfWorking, ways},
		{stockupdate.ToolVersions, map[string][]byte{stock.ToolVersionsFile: tools}},
	} {
		b, err := stockbuild.Build(u.series, u.payload, name, published)
		if err != nil {
			log.Fatal(err)
		}
		file := filepath.Join(*out, stockbuild.FileName(u.series, safe(name)))
		if err := os.WriteFile(file, b, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Println(file)
	}
}

// safe keeps a version usable in a file name.
func safe(s string) string { return strings.ReplaceAll(s, "/", "-") }

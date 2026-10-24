package runtimeimages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repositoryList(t *testing.T) *List {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "stock", "tool-versions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Every runtime image the build runs is in the list, by digest, at the
// build's line; the stock packs' lines are there too.
func TestTheStockListNamesEveryRuntimeImageByDigest(t *testing.T) {
	t.Parallel()
	l := repositoryList(t)
	im, err := l.Images()
	if err != nil {
		t.Fatal(err)
	}
	for tool, ref := range map[string]string{ToolSh: im.Sh, ToolDF: im.DF, ToolStaticServer: im.StaticServer, ToolPrivateDocker: im.PrivateDocker} {
		if !strings.Contains(ref, "@sha256:") {
			t.Errorf("%s: %q names no digest", tool, ref)
		}
	}
	if !l.HasLine("postgres", "16") || l.HasLine("postgres", "15") {
		t.Error("HasLine does not read the list")
	}
}

// A list that names an image by tag alone, a line with no exact version, a
// key the format does not have, or one missing a runtime tool the build
// runs is refused, never half read.
func TestAListThatCannotBeTrustedIsRefused(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]string{
		"a tag alone":      "tools:\n  apptown-sh:\n    \"1\": {version: v1, images: {run: \"alpine/git:v1\"}}\n",
		"no exact version": "tools:\n  node:\n    \"24\": {}\n",
		"an unknown key":   "tools:\n  node:\n    \"24\": {version: 24.1.0, latest: true}\n",
		"no tools":         "kind: ToolVersions\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	l, err := Parse([]byte("tools:\n  node:\n    \"24\": {version: 24.1.0}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Images(); err == nil {
		t.Error("a list without the runtime images gave a set")
	}
}

// Outside a test binary, a getter before Set panics, naming the start step
// that was missed: nothing falls back to a copy.
func TestAProcessThatDidNotLoadItsImagesStops(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != notSet {
			t.Fatalf("recovered %v, want the start step named", r)
		}
	}()
	chosen(nil, false)
	t.Fatal("no panic")
}

package stockupdate_test

import (
	"archive/tar"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/stockupdate"
	"github.com/flokkit/apptown/internal/stockupdate/stockbuild"
)

var at = time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

const header = "# kept\napiVersion: apptown.dev/v1\nkind: WaysOfWorkingUpdate\nworksWith: \">=1.0.0\"\n"

// An update file carries its content and a header naming it: what is built
// is what is read, the build's name and time stamped into the header, the
// rest of the header and every other file as written.
func TestAnUpdateReadsBackAsBuilt(t *testing.T) {
	t.Parallel()
	payload := map[string][]byte{"update.yaml": []byte(header), "packs.yaml": []byte("packs: []\n"), "seeds/a/.apptown/app.yaml": []byte("version: 1\n")}
	b, err := stockbuild.Build(stockupdate.WaysOfWorking, payload, "v1.0.0", at)
	if err != nil {
		t.Fatal(err)
	}
	u, err := stockupdate.Parse(stockupdate.WaysOfWorking, b)
	if err != nil {
		t.Fatal(err)
	}
	if u.Header.Name != "v1.0.0" || !u.Header.Published.Equal(at) || u.Header.WorksWith != ">=1.0.0" || u.Header.APIVersion != "apptown.dev/v1" {
		t.Fatalf("header %+v", u.Header)
	}
	for p, want := range payload {
		got, err := fs.ReadFile(u.Content, p)
		if err != nil {
			t.Fatal(err)
		}
		if p == "update.yaml" {
			if !strings.Contains(string(got), "# kept") || !strings.Contains(string(got), "name: v1.0.0") {
				t.Errorf("stamped header:\n%s", got)
			}
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
}

// What is not an update of the series asked for is refused, never read as
// an empty one.
func TestAFileThatIsNotAnUpdateIsRefused(t *testing.T) {
	t.Parallel()
	good, err := stockbuild.Build(stockupdate.WaysOfWorking, map[string][]byte{"update.yaml": []byte(header)}, "v1.0.0", at)
	if err != nil {
		t.Fatal(err)
	}
	var noContent bytes.Buffer
	tw := tar.NewWriter(&noContent)
	_ = tw.WriteHeader(&tar.Header{Name: "other", Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	for name, raw := range map[string][]byte{
		"not a tar":        []byte("hello"),
		"no update.tar.gz": noContent.Bytes(),
		"the other series": good,
	} {
		series := stockupdate.WaysOfWorking
		if name == "the other series" {
			series = stockupdate.ToolVersions
		}
		if _, err := stockupdate.Parse(series, raw); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	for name, hdr := range map[string]string{
		"wrong kind":             "kind: ToolVersions\nworksWith: \">=1.0.0\"\n",
		"no works-with":          "kind: WaysOfWorkingUpdate\n",
		"a range it cannot read": "kind: WaysOfWorkingUpdate\nworksWith: \"~1\"\n",
	} {
		if _, err := stockbuild.Build(stockupdate.WaysOfWorking, map[string][]byte{"update.yaml": []byte(hdr)}, "v1.0.0", at); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

// A release judges an update's range on its numeric version, a release
// candidate counting as its release; a maintainer's build cannot place itself
// and says so.
func TestABuildJudgesTheRangeItCan(t *testing.T) {
	t.Parallel()
	h := stockupdate.Header{WorksWith: ">=1.0.0 <2.0.0"}
	for version, want := range map[string]error{
		"v1.0.0":      nil,
		"v1.0.0-rc.8": nil,
		"v1.4.2":      nil,
		"v0.9.0":      stockupdate.ErrIncompatible,
		"v2.0.0":      stockupdate.ErrIncompatible,
	} {
		j, err := h.Judge(version)
		if !errors.Is(err, want) || (err == nil && j != stockupdate.Works) {
			t.Errorf("%s: %v %v, want %v", version, j, err, want)
		}
	}
	if j, err := h.Judge("dev-0123456789ab"); err != nil || j != stockupdate.NotJudged {
		t.Errorf("a maintainer's build: %v %v", j, err)
	}
}

// The shipped folder's newest update of a series is the one read; the other
// series is not its business, and an unreadable file of the series is an
// error, not skipped.
func TestTheNewestShippedUpdateOfASeriesIsRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(series stockupdate.Series, name string, published time.Time, hdr string) {
		b, err := stockbuild.Build(series, map[string][]byte{series.HeaderPath(): []byte(hdr)}, name, published)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stockbuild.FileName(series, name)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stockupdate.Shipped(dir, stockupdate.WaysOfWorking); !errors.Is(err, stockupdate.ErrNoUpdate) {
		t.Fatalf("an empty folder: %v", err)
	}
	write(stockupdate.WaysOfWorking, "old", at, header)
	write(stockupdate.WaysOfWorking, "new", at.Add(time.Hour), header)
	write(stockupdate.ToolVersions, "tools", at.Add(2*time.Hour), "kind: ToolVersions\nworksWith: \">=1.0.0\"\n")
	u, err := stockupdate.Shipped(dir, stockupdate.WaysOfWorking)
	if err != nil {
		t.Fatal(err)
	}
	if u.Header.Name != "new" {
		t.Fatalf("read %s, want the newest", u.Header.Name)
	}
	if err := os.WriteFile(filepath.Join(dir, stockbuild.FileName(stockupdate.WaysOfWorking, "broken")), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := stockupdate.Shipped(dir, stockupdate.WaysOfWorking); err == nil {
		t.Fatal("an unreadable update of the series was skipped")
	}
}

// A binary finds its updates in share/apptown/updates beside its bin folder.
func TestABinaryFindsItsUpdatesBesideItsFolder(t *testing.T) {
	t.Parallel()
	if got, want := stockupdate.Dir("/opt/apptown/bin/apptownd"), "/opt/apptown/share/apptown/updates"; got != want {
		t.Fatalf("%s, want %s", got, want)
	}
}

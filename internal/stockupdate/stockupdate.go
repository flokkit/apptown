// Package stockupdate reads and writes the files AppTown's stock travels in:
// a ways-of-working update and a tool versions update. A release carries one
// of each beside its binaries (share/apptown/updates/), and AppTown reads its
// stock from them at start: there is no compiled-in copy.
//
// An update file is a tar holding update.tar.gz, the content; a signature
// bundle joins it beside the content when updates are delivered between
// releases. The content's header document (update.yaml for ways of working,
// tool-versions.yaml for tool versions) names the update, when it was
// published and the AppTown versions it works with.
//
// The package knows nothing of what the content means: its owners read it.
package stockupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing/fstest"
	"time"

	"gopkg.in/yaml.v3"
)

// Series is which kind of update a file is.
type Series string

const (
	WaysOfWorking Series = "ways-of-working"
	ToolVersions  Series = "tool-versions"
)

// Kind is the header's kind for each series.
func (s Series) Kind() string {
	switch s {
	case WaysOfWorking:
		return "WaysOfWorkingUpdate"
	case ToolVersions:
		return "ToolVersions"
	}
	return ""
}

// HeaderPath is where a series' header document sits in its content.
func (s Series) HeaderPath() string {
	if s == ToolVersions {
		return "tool-versions.yaml"
	}
	return "update.yaml"
}

// FileSuffix ends every update file's name.
const FileSuffix = ".update"

// Content is the one member of an update file a reader reads.
const Content = "update.tar.gz"

// Header is what an update says about itself.
type Header struct {
	APIVersion string    `yaml:"apiVersion"`
	Kind       string    `yaml:"kind"`
	Name       string    `yaml:"name"`
	Published  time.Time `yaml:"published"`
	WorksWith  string    `yaml:"worksWith"`
}

// Update is a file read: its series, header and content.
type Update struct {
	Series Series
	Header Header
	// Content is the update's files, the header document among them.
	Content fs.FS
	// Path is the file it was read from.
	Path string
}

// Read reads an update file of series.
func Read(series Series, file string) (*Update, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	u, err := Parse(series, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	u.Path = file
	return u, nil
}

// Parse reads an update file's bytes.
func Parse(series Series, raw []byte) (*Update, error) {
	var inner []byte
	found := false
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not an update file: %w", err)
		}
		if h.Name != Content {
			continue
		}
		if inner, err = io.ReadAll(tr); err != nil {
			return nil, err
		}
		found = true
	}
	if !found {
		return nil, fmt.Errorf("not an update file: it holds no %s", Content)
	}
	gz, err := gzip.NewReader(bytes.NewReader(inner))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Content, err)
	}
	files := fstest.MapFS{}
	tr = tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", Content, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(h.Name)
		if !fs.ValidPath(name) {
			return nil, fmt.Errorf("%s: %q is not a path inside the update", Content, h.Name)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files[name] = &fstest.MapFile{Data: b, Mode: 0o644, ModTime: h.ModTime}
	}
	hdr, ok := files[series.HeaderPath()]
	if !ok {
		return nil, fmt.Errorf("%s update without %s", series, series.HeaderPath())
	}
	h, err := readHeader(series, hdr.Data)
	if err != nil {
		return nil, err
	}
	return &Update{Series: series, Header: h, Content: files}, nil
}

func readHeader(series Series, raw []byte) (Header, error) {
	var h Header
	if err := yaml.Unmarshal(raw, &h); err != nil {
		return h, fmt.Errorf("%s: %w", series.HeaderPath(), err)
	}
	switch {
	case h.Kind != series.Kind():
		return h, fmt.Errorf("%s: kind %q, want %s", series.HeaderPath(), h.Kind, series.Kind())
	case h.Name == "":
		return h, fmt.Errorf("%s: no name", series.HeaderPath())
	case h.Published.IsZero():
		return h, fmt.Errorf("%s: no publication time", series.HeaderPath())
	}
	if _, err := parseRange(h.WorksWith); err != nil {
		return h, fmt.Errorf("%s: worksWith: %w", series.HeaderPath(), err)
	}
	return h, nil
}

// Dir is where a binary at exe finds the updates it shipped with:
// <exe's folder>/../share/apptown/updates.
func Dir(exe string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "share", "apptown", "updates")
}

// ErrNoUpdate is a directory with no update file of the series asked for.
var ErrNoUpdate = errors.New("no update file")

// Shipped reads the newest update file of series in dir, by publication
// time. Files of other series are not read; a file of this series that
// does not read is an error, never skipped.
func Shipped(dir string, series Series) (*Update, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var newest *Update
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, string(series)+"-") || !strings.HasSuffix(n, FileSuffix) {
			continue
		}
		u, err := Read(series, filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		if newest == nil || u.Header.Published.After(newest.Header.Published) {
			newest = u
		}
	}
	if newest == nil {
		return nil, fmt.Errorf("%w of %s in %s", ErrNoUpdate, series, dir)
	}
	return newest, nil
}

// Judgement is what a build can say about an update's worksWith.
type Judgement int

const (
	// Works: the build's version is in the range.
	Works Judgement = iota
	// NotJudged: the build has no release version to place in a range (a
	// maintainer's dev- build). Its readers still validate every item.
	NotJudged
)

// ErrIncompatible is an update whose worksWith excludes this build.
var ErrIncompatible = errors.New("does not work with this AppTown")

// Judge says whether a build of version can read the update. A dev- build
// is NotJudged; a release outside the range is ErrIncompatible.
func (h Header) Judge(version string) (Judgement, error) {
	if strings.HasPrefix(version, "dev-") {
		return NotJudged, nil
	}
	r, err := parseRange(h.WorksWith)
	if err != nil {
		return 0, err
	}
	v, err := parseVersion(version)
	if err != nil {
		return 0, fmt.Errorf("this AppTown's version %q: %w", version, err)
	}
	if compare(v, r.min) < 0 || (r.max != nil && compare(v, *r.max) >= 0) {
		return 0, fmt.Errorf("%w: it needs AppTown %s; this is %s", ErrIncompatible, h.WorksWith, version)
	}
	return Works, nil
}

type triple [3]int

type versionRange struct {
	min triple
	max *triple
}

// parseRange reads ">=X.Y.Z" with an optional " <A.B.C".
func parseRange(s string) (versionRange, error) {
	var r versionRange
	f := strings.Fields(s)
	if len(f) == 0 || len(f) > 2 || !strings.HasPrefix(f[0], ">=") {
		return r, fmt.Errorf("%q is not >=X.Y.Z with an optional <A.B.C", s)
	}
	min, err := parseVersion(strings.TrimPrefix(f[0], ">="))
	if err != nil {
		return r, err
	}
	r.min = min
	if len(f) == 2 {
		if !strings.HasPrefix(f[1], "<") {
			return r, fmt.Errorf("%q is not >=X.Y.Z with an optional <A.B.C", s)
		}
		max, err := parseVersion(strings.TrimPrefix(f[1], "<"))
		if err != nil {
			return r, err
		}
		r.max = &max
	}
	return r, nil
}

// parseVersion reads the numeric core of v1.2.3 or 1.2.3-rc.4: a release
// candidate of 1.2.3 counts as 1.2.3.
func parseVersion(s string) (triple, error) {
	var t triple
	core, _, _ := strings.Cut(strings.TrimPrefix(s, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return t, fmt.Errorf("%q is not a version X.Y.Z", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return t, fmt.Errorf("%q is not a version X.Y.Z", s)
		}
		t[i] = n
	}
	return t, nil
}

func compare(a, b triple) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

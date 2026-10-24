// Package runtimeimages reads a tool versions list (stock/tool-versions.yaml,
// shipped as a tool versions update) and holds the images this process runs
// for itself: the shell guidance checks run in, the df behind disk reads,
// the static server of release images, and the private Docker daemon.
//
// They are facts of the build: apptownd and apptown-worker read them from
// the tool versions update shipped beside them, once, before anything runs,
// and Set them. Nothing is compiled in; a getter before Set panics, naming
// the start step that was missed. A test binary that has not Set them reads
// stock/tool-versions.yaml from the repository it runs in: tests may read
// stock directly, and a binary never takes that path.
package runtimeimages

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

// Entry is what one line names exactly.
type Entry struct {
	Version   string            `yaml:"version"`
	Label     string            `yaml:"label,omitempty"`
	Images    map[string]string `yaml:"images,omitempty"`
	EndOfLife string            `yaml:"endOfLife,omitempty"`
}

// List is a tool versions list: tool → line → entry.
type List struct {
	Tools map[string]map[string]Entry `yaml:"tools"`
}

// The runtime tools and the line this build runs of each. A line is the
// build's choice; the list says what it means exactly.
const (
	ToolSh            = "apptown-sh"
	ToolDF            = "apptown-df"
	ToolStaticServer  = "static-server"
	ToolPrivateDocker = "private-docker"
)

var runtimeLines = map[string]string{ToolSh: "1", ToolDF: "1", ToolStaticServer: "2", ToolPrivateDocker: "27"}

// Parse reads a tool versions document and validates it: every entry has an
// exact version, and every image names a digest.
func Parse(raw []byte) (*List, error) {
	// The header's keys are the update's (internal/stockupdate); any other
	// key is a mistake, refused rather than ignored.
	var doc struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
		Name       string `yaml:"name"`
		Published  string `yaml:"published"`
		WorksWith  string `yaml:"worksWith"`
		List       `yaml:",inline"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("tool versions: %w", err)
	}
	l := doc.List
	if len(l.Tools) == 0 {
		return nil, errors.New("tool versions: no tools")
	}
	for _, tool := range sortedKeys(l.Tools) {
		for _, line := range sortedKeys(l.Tools[tool]) {
			e := l.Tools[tool][line]
			if strings.TrimSpace(e.Version) == "" {
				return nil, fmt.Errorf("tool versions: %s %s: no exact version", tool, line)
			}
			for _, role := range sortedKeys(e.Images) {
				if !strings.Contains(e.Images[role], "@sha256:") {
					return nil, fmt.Errorf("tool versions: %s %s: image %s %q names no digest", tool, line, role, e.Images[role])
				}
			}
		}
	}
	return &l, nil
}

// HasLine says whether the list names tool at line.
func (l *List) HasLine(tool, line string) bool {
	_, ok := l.Tools[tool][line]
	return ok
}

// Images is the set of runtime images.
type Images struct {
	Sh, DF, StaticServer, PrivateDocker string
}

// Images reads the runtime images from the list: each runtime tool at this
// build's line, its run image.
func (l *List) Images() (Images, error) {
	get := func(tool string) (string, error) {
		line := runtimeLines[tool]
		e, ok := l.Tools[tool][line]
		if !ok {
			return "", fmt.Errorf("tool versions: no %s line %s, which this AppTown runs", tool, line)
		}
		img := e.Images["run"]
		if img == "" {
			return "", fmt.Errorf("tool versions: %s %s has no run image", tool, line)
		}
		return img, nil
	}
	var im Images
	var err error
	for _, f := range []struct {
		tool string
		to   *string
	}{{ToolSh, &im.Sh}, {ToolDF, &im.DF}, {ToolStaticServer, &im.StaticServer}, {ToolPrivateDocker, &im.PrivateDocker}} {
		if *f.to, err = get(f.tool); err != nil {
			return Images{}, err
		}
	}
	return im, nil
}

var current atomic.Pointer[Images]

// Set records this process's runtime images. It is called once at start,
// before anything that runs an image.
func Set(im Images) { current.Store(&im) }

func get() *Images { return chosen(current.Load(), testing.Testing()) }

// chosen is the set a getter answers: the one Set, else, in a test binary
// only, the repository's; else nothing was loaded, and it panics.
func chosen(set *Images, inTest bool) *Images {
	if set == nil && inTest {
		set = fromRepository()
	}
	if set == nil {
		panic(notSet)
	}
	return set
}

const notSet = "runtime images not set: the process reads them from its shipped tool versions at start (runtimeimages.Set), before anything runs"

var repository struct {
	once sync.Once
	im   *Images
}

// fromRepository is a test binary's set: stock/tool-versions.yaml of the
// repository the test runs in (go test runs in the package's folder).
func fromRepository() *Images {
	repository.once.Do(func() {
		dir, err := os.Getwd()
		for err == nil {
			raw, rerr := os.ReadFile(filepath.Join(dir, "stock", "tool-versions.yaml"))
			if rerr == nil {
				l, perr := Parse(raw)
				if perr != nil {
					panic(perr)
				}
				im, ierr := l.Images()
				if ierr != nil {
					panic(ierr)
				}
				repository.im = &im
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return
			}
			dir = parent
		}
	})
	return repository.im
}

// Sh is the image every sh check runs in: alpine with git.
func Sh() string { return get().Sh }

// DF is the image disk reads through Docker run df in.
func DF() string { return get().DF }

// StaticServer serves static contract output in deployments and local compose.
func StaticServer() string { return get().StaticServer }

// PrivateDocker is the private Docker daemon's image.
func PrivateDocker() string { return get().PrivateDocker }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

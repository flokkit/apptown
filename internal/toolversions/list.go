// Package toolversions reads a tool versions list (stock/tool-versions.yaml,
// shipped as a tool versions update) and owns the one a process uses:
// Versions answers its current list as an immutable Snapshot, the images
// AppTown runs for itself among it (the shell guidance checks run in, the
// df behind disk reads, the static server of release images, the private
// Docker daemon). apptownd and apptown-worker fill it at start from the
// update shipped beside them; owners take it, or the narrow Source, and
// each unit of work takes one Snapshot at its start. Nothing is compiled
// in and nothing is global.
package toolversions

import (
	"errors"
	"fmt"
	"sort"
	"strings"

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

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

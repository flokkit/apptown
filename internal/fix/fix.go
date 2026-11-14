// Package fix interprets dependency classification declarations and fix kinds.
package fix

import (
	"encoding/json"
	"fmt"
	"github.com/flokkit/apptown/internal/check"
	"io"
	"path"
	"reflect"
	"slices"
	"strings"
)

type Declaration struct {
	Kind      string   `json:"kind" yaml:"kind"`
	Ecosystem string   `json:"ecosystem" yaml:"ecosystem"`
	Files     []string `json:"files" yaml:"files"`
}

// File records the observed classification of one changed path.
type File struct {
	Path  string `json:"path"`
	Class string `json:"class"`
}

// Classify labels paths using adopted dependency globs and observed input paths.
func Classify(paths []string, declarations []Declaration, inputs ...map[string]any) []File {
	out := make([]File, 0, len(paths))
	for _, name := range paths {
		class := "other"
		for _, d := range declarations {
			for _, glob := range d.Files {
				full, _ := path.Match(glob, name)
				base, _ := path.Match(glob, path.Base(name))
				if full || base {
					class = "dependency"
				}
			}
		}
		for _, side := range inputs {
			for key := range side {
				if strings.SplitN(key, ":", 2)[0] == name {
					class = "toolchain"
				}
			}
		}
		out = append(out, File{Path: name, Class: class})
	}
	return out
}
func Parse(raw string) ([]Declaration, error) {
	var out []Declaration
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("fixes: expected one array")
	}
	for _, d := range out {
		k, ok := Lookup(d.Kind)
		if !ok {
			return nil, fmt.Errorf("fix kind %q is not supported", d.Kind)
		}
		if k.PublishedDeclaration {
			return nil, fmt.Errorf("fix %s is declared by a published pack version, not guidance", d.Kind)
		}
		fields := map[string]any{"ecosystem": d.Ecosystem, "files": d.Files}
		for name, value := range fields {
			if !reflect.ValueOf(value).IsZero() && !slices.ContainsFunc(k.DeclarationFields, func(f DeclarationField) bool { return f.Name == name }) {
				return nil, fmt.Errorf("fix %s does not support %s", d.Kind, name)
			}
		}
		for _, field := range k.DeclarationFields {
			if field.Required && reflect.ValueOf(fields[field.Name]).IsZero() {
				return nil, fmt.Errorf("fix %s needs %s", d.Kind, field.Name)
			}
		}
	}
	return out, nil
}

// Targets decodes the caller's recorded findings from workflow inputs.
func Targets(inputs string) ([]check.Finding, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inputs), &values); err != nil {
		return nil, err
	}
	raw, ok := values["targets"]
	if !ok {
		return nil, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}
	var targets []check.Finding
	err := json.Unmarshal([]byte(encoded), &targets)
	return targets, err
}

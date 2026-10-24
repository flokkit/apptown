// Package stockbuild writes the update files a release ships
// (internal/stockupdate reads them): scripts/stock-files.go at build, and
// tests. The binaries only read updates.
package stockbuild

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/flokkit/apptown/internal/stockupdate"
	"gopkg.in/yaml.v3"
)

// Build writes an update file of series from the files in payload, which
// must hold the series' header document; name and published are stamped
// into that header, the rest of it kept as written.
func Build(series stockupdate.Series, payload map[string][]byte, name string, published time.Time) ([]byte, error) {
	raw, ok := payload[series.HeaderPath()]
	if !ok {
		return nil, fmt.Errorf("%s update: no %s", series, series.HeaderPath())
	}
	stamped, err := stamp(raw, name, published)
	if err != nil {
		return nil, fmt.Errorf("%s update: %s: %w", series, series.HeaderPath(), err)
	}
	files := make(map[string][]byte, len(payload))
	for p, b := range payload {
		files[p] = b
	}
	files[series.HeaderPath()] = stamped
	var inner bytes.Buffer
	gz := gzip.NewWriter(&inner)
	if err := writeTar(gz, files, published); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	var outer bytes.Buffer
	if err := writeTar(&outer, map[string][]byte{stockupdate.Content: inner.Bytes()}, published); err != nil {
		return nil, err
	}
	// What is written reads back, or it is not written.
	if _, err := stockupdate.Parse(series, outer.Bytes()); err != nil {
		return nil, fmt.Errorf("%s update: %w", series, err)
	}
	return outer.Bytes(), nil
}

// stamp sets name and published in a header document, keeping its other
// keys and comments.
func stamp(raw []byte, name string, published time.Time) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a mapping")
	}
	m := doc.Content[0]
	set := func(key, value string) {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				m.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Value: value}
				return
			}
		}
		// After kind, where a reader looks for it.
		at := len(m.Content)
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == "kind" {
				at = i + 2
			}
		}
		pair := []*yaml.Node{{Kind: yaml.ScalarNode, Value: key}, {Kind: yaml.ScalarNode, Value: value}}
		m.Content = append(m.Content[:at], append(pair, m.Content[at:]...)...)
	}
	set("published", published.UTC().Format(time.RFC3339))
	set("name", name)
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return out.Bytes(), enc.Close()
}

func writeTar(w io.Writer, files map[string][]byte, at time.Time) error {
	names := make([]string, 0, len(files))
	for p := range files {
		names = append(names, p)
	}
	sort.Strings(names)
	tw := tar.NewWriter(w)
	for _, p := range names {
		if err := tw.WriteHeader(&tar.Header{Name: p, Mode: 0o644, Size: int64(len(files[p])), ModTime: at.UTC(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := tw.Write(files[p]); err != nil {
			return err
		}
	}
	return tw.Close()
}

// FileName is the name an update file of series and name is written as.
func FileName(series stockupdate.Series, name string) string {
	return string(series) + "-" + name + stockupdate.FileSuffix
}

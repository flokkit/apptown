// Package fix interprets declared mechanical fixes and independently judges their results.
package fix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/check"
	"golang.org/x/mod/semver"
)

type Declaration struct {
	Kind      string   `json:"kind" yaml:"kind"`
	AppliesTo []string `json:"appliesTo" yaml:"appliesTo"`
	Ecosystem string   `json:"ecosystem" yaml:"ecosystem"`
	Run       string   `json:"run" yaml:"run"`
	RunFor    string   `json:"runFor" yaml:"runFor"`
	List      string   `json:"list" yaml:"list"`
	Files     []string `json:"files" yaml:"files"`
}

type Listing struct {
	Ecosystem  string         `json:"ecosystem"`
	Package    string         `json:"package"`
	Version    string         `json:"version"`
	Source     string         `json:"source"`
	Attributes map[string]any `json:"attributes"`
}
type Move struct {
	Ecosystem string `json:"ecosystem"`
	Package   string `json:"package"`
	From      string `json:"from"`
	To        string `json:"to"`
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
		fields := map[string]any{"ecosystem": d.Ecosystem, "run": d.Run, "runFor": d.RunFor, "list": d.List, "files": d.Files}
		for name, value := range fields {
			if !reflect.ValueOf(value).IsZero() && !slices.ContainsFunc(k.DeclarationFields, func(f DeclarationField) bool { return f.Name == name }) {
				return nil, fmt.Errorf("fix %s does not support %s", d.Kind, name)
			}
		}
		if err := check.ValidateAppliesTo(d.AppliesTo); err != nil {
			return nil, err
		}
		for _, field := range k.DeclarationFields {
			if field.Required && reflect.ValueOf(fields[field.Name]).IsZero() {
				return nil, fmt.Errorf("fix %s needs %s", d.Kind, field.Name)
			}
		}
	}
	return out, nil
}
func UpdateCommand(d Declaration, packages []string, targeted bool) (string, error) {
	if !targeted {
		return d.Run, nil
	}
	if d.RunFor == "" {
		return "", fmt.Errorf("%s fix has no runFor", d.Ecosystem)
	}
	sort.Strings(packages)
	quoted := make([]string, 0, len(packages))
	for _, p := range packages {
		if p == "" || strings.HasPrefix(p, "-") || strings.ContainsAny(p, "\x00\n\r") {
			return "", fmt.Errorf("invalid target package %q", p)
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(p, "'", "'\"'\"'")+"'")
	}
	if len(quoted) == 0 {
		return "", fmt.Errorf("targeted fix has no packages")
	}
	if !strings.Contains(d.RunFor, "{packages}") {
		return "", fmt.Errorf("%s runFor has no {packages}", d.Ecosystem)
	}
	return strings.ReplaceAll(d.RunFor, "{packages}", strings.Join(quoted, " ")), nil
}
func ReadListing(raw []byte) ([]Listing, error) {
	var out []Listing
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	for {
		var row Listing
		err := dec.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if row.Ecosystem == "" || row.Package == "" || row.Version == "" || row.Source == "" || row.Attributes == nil {
			return nil, fmt.Errorf("listing needs ecosystem, package, version and source")
		}
		if _, err := version(row.Version); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}
func version(v string) (string, error) {
	v = "v" + strings.TrimPrefix(v, "v")
	if !semver.IsValid(v) || semver.Canonical(v) != strings.SplitN(v, "+", 2)[0] {
		return "", fmt.Errorf("non-semver version %q", v)
	}
	return v, nil
}
func CompareUpdate(before, after []Listing, bound string, targets []check.Finding) ([]Move, error) {
	if bound != "patch" && bound != "minor" {
		return nil, fmt.Errorf("no dependency update bound")
	}
	old := map[string]Listing{}
	next := map[string]Listing{}
	order := func(packages []Listing) ([]Listing, error) {
		type installed struct {
			listing    Listing
			attributes string
		}
		rows := make([]installed, 0, len(packages))
		for _, p := range packages {
			raw, err := json.Marshal(p.Attributes)
			if err != nil {
				return nil, fmt.Errorf("%s attributes: %w", p.Package, err)
			}
			rows = append(rows, installed{p, string(raw)})
		}
		slices.SortFunc(rows, func(a, b installed) int {
			for _, pair := range [][2]string{{a.listing.Ecosystem, b.listing.Ecosystem}, {a.listing.Package, b.listing.Package}, {a.listing.Source, b.listing.Source}, {a.attributes, b.attributes}} {
				if c := strings.Compare(pair[0], pair[1]); c != 0 {
					return c
				}
			}
			av, _ := version(a.listing.Version)
			bv, _ := version(b.listing.Version)
			return semver.Compare(av, bv)
		})
		out := make([]Listing, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.listing)
		}
		return out, nil
	}
	var err error
	before, err = order(before)
	if err != nil {
		return nil, err
	}
	after, err = order(after)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	key := func(p Listing) string {
		base := p.Ecosystem + "\x00" + p.Package
		counts[base]++
		return base + "\x00" + strconv.Itoa(counts[base])
	}
	for _, p := range before {
		if _, err := version(p.Version); err != nil {
			return nil, err
		}
		old[key(p)] = p
	}
	counts = map[string]int{}
	var moved []Move
	for _, p := range after {
		v, err := version(p.Version)
		if err != nil {
			return nil, err
		}
		k := key(p)
		next[k] = p
		b, ok := old[k]
		if !ok {
			return nil, fmt.Errorf("new package %s", p.Package)
		}
		if b.Source != p.Source {
			return nil, fmt.Errorf("%s changed source", p.Package)
		}
		if !reflect.DeepEqual(b.Attributes, p.Attributes) {
			return nil, fmt.Errorf("%s changed attributes", p.Package)
		}
		bv, _ := version(b.Version)
		if bv == v {
			continue
		}
		if semver.Compare(v, bv) <= 0 {
			return nil, fmt.Errorf("%s did not move forward", p.Package)
		}
		if semver.Major(v) != semver.Major(bv) || ((bound == "patch" || semver.Major(bv) == "v0") && semver.MajorMinor(v) != semver.MajorMinor(bv)) {
			return nil, fmt.Errorf("%s exceeds the %s update bound", p.Package, bound)
		}
		moved = append(moved, Move{p.Ecosystem, p.Package, b.Version, p.Version})
	}
	if len(old) != len(next) {
		return nil, fmt.Errorf("package set changed")
	}
	for _, t := range targets {
		if t.Package == nil {
			continue
		}
		fixed, err := version(t.Package.FixedIn)
		found := false
		for _, p := range next {
			if p.Package != t.Package.Name || p.Ecosystem != t.Package.Ecosystem {
				continue
			}
			found = true
			v, _ := version(p.Version)
			if err != nil || semver.Compare(v, fixed) < 0 {
				return nil, fmt.Errorf("%s is below FixedIn %s", p.Package, t.Package.FixedIn)
			}
		}
		if !found {
			return nil, fmt.Errorf("%s has not reached FixedIn %s", t.Package.Name, t.Package.FixedIn)
		}
	}
	sort.Slice(moved, func(i, j int) bool { return moved[i].Ecosystem+moved[i].Package < moved[j].Ecosystem+moved[j].Package })
	return moved, nil
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

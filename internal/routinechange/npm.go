// Package routinechange classifies bounded changes to dependency declarations.
// A candidate is not approval or execution evidence: installed observations and
// the owning workflow's source and authority checks are still required.
package routinechange

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const Version = "npm-fixed-graph-v1"

// NPMSnapshot contains the exact committed input bytes, not a display diff.
type NPMSnapshot struct{ Manifest, Lock []byte }

// NPMPackage is the expected identity at an installation path. Changed says
// independently observed lifecycle/platform behavior must also be refused.
type NPMPackage struct {
	Path, Name, Version, Integrity string
	Changed                        bool
}

// NPMCandidate records both sides of a semantic comparison. The validator must
// install each exact input independently before this can support a classification.
type NPMCandidate struct{ Before, After []NPMPackage }

// CompareNPM supports root manifests and npm lockfile v3 with an unchanged graph.
// Bounds is "patch" or "minor". Unsupported or unexplained input is an error;
// callers report that the change needs a person, never silently omit a field.
func CompareNPM(before, after NPMSnapshot, bounds string) (NPMCandidate, error) {
	var out NPMCandidate
	if bounds != "patch" && bounds != "minor" {
		return out, errors.New("dependency updates are not permitted by this rule")
	}
	bm, err := object(before.Manifest)
	if err != nil {
		return out, fmt.Errorf("base manifest: %w", err)
	}
	am, err := object(after.Manifest)
	if err != nil {
		return out, fmt.Errorf("candidate manifest: %w", err)
	}
	if err = declarations(bm, am, bounds); err != nil {
		return out, fmt.Errorf("manifest: %w", err)
	}
	bl, err := object(before.Lock)
	if err != nil {
		return out, fmt.Errorf("base lock: %w", err)
	}
	al, err := object(after.Lock)
	if err != nil {
		return out, fmt.Errorf("candidate lock: %w", err)
	}
	if bl["lockfileVersion"] != json.Number("3") || al["lockfileVersion"] != json.Number("3") {
		return out, errors.New("only npm lockfile version 3 is supported")
	}
	bp, bok := bl["packages"].(map[string]any)
	ap, aok := al["packages"].(map[string]any)
	if !bok || !aok || !sameKeys(bp, ap) {
		return out, errors.New("the installed package graph changed or is missing")
	}
	delete(bl, "packages")
	delete(al, "packages")
	if !reflect.DeepEqual(bl, al) {
		return out, errors.New("unexplained lock metadata changed")
	}
	var paths []string
	for path := range bp {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	changed := false
	for _, path := range paths {
		b, ok := bp[path].(map[string]any)
		if !ok {
			return out, errors.New("invalid base package record")
		}
		a, ok := ap[path].(map[string]any)
		if !ok {
			return out, errors.New("invalid candidate package record")
		}
		if path == "" {
			if err = declarations(b, a, bounds); err != nil {
				return out, fmt.Errorf("lock root: %w", err)
			}
			for _, key := range dependencyFields {
				if !reflect.DeepEqual(bm[key], b[key]) || !reflect.DeepEqual(am[key], a[key]) {
					return out, errors.New("manifest and lock root declarations differ")
				}
			}
			continue
		}
		name, err := packageName(path)
		if err != nil {
			return out, err
		}
		old, err := packageRecord(path, name, b)
		if err != nil {
			return out, err
		}
		next, err := packageRecord(path, name, a)
		if err != nil {
			return out, err
		}
		if err = forward(old.Version, next.Version, bounds, false); err != nil {
			return out, fmt.Errorf("%s: %w", path, err)
		}
		old.Changed = old.Version != next.Version
		next.Changed = old.Changed
		changed = changed || old.Changed
		if !old.Changed && old.Integrity != next.Integrity {
			return out, fmt.Errorf("%s changed content without changing version", path)
		}
		if old.Changed && (behavior(b) || behavior(a)) {
			return out, fmt.Errorf("%s has install or platform behavior requiring a person", path)
		}
		bc, ac := clone(b), clone(a)
		for _, k := range []string{"version", "resolved", "integrity"} {
			delete(bc, k)
			delete(ac, k)
		}
		if !reflect.DeepEqual(bc, ac) {
			return out, fmt.Errorf("%s changed graph or unexplained package metadata", path)
		}
		out.Before = append(out.Before, old)
		out.After = append(out.After, next)
	}
	if _, ok := bp[""]; !ok {
		return out, errors.New("lock root is missing")
	}
	if !changed {
		return out, errors.New("no dependency version changed")
	}
	return out, nil
}

var dependencyFields = []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"}

func declarations(before, after map[string]any, bounds string) error {
	b, a := clone(before), clone(after)
	for _, m := range []map[string]any{b, a} {
		if _, ok := m["workspaces"]; ok {
			return errors.New("npm workspaces require a person")
		}
		if manager, ok := m["packageManager"]; ok {
			v, isString := manager.(string)
			if !isString || !strings.HasPrefix(v, "npm@") {
				return errors.New("another declared package manager requires a person")
			}
		}
	}
	for _, field := range dependencyFields {
		bv, be := b[field]
		av, ae := a[field]
		if be != ae {
			return fmt.Errorf("%s presence changed", field)
		}
		if !be {
			continue
		}
		bm, bok := bv.(map[string]any)
		am, aok := av.(map[string]any)
		if !bok || !aok || !sameKeys(bm, am) {
			return fmt.Errorf("%s package set changed or is malformed", field)
		}
		for name, old := range bm {
			bs, bok := old.(string)
			as, aok := am[name].(string)
			if !bok || !aok {
				return errors.New("dependency versions must be strings")
			}
			if err := forward(bs, as, bounds, true); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		delete(b, field)
		delete(a, field)
	}
	if !reflect.DeepEqual(b, a) {
		return errors.New("non-dependency manifest fields changed")
	}
	return nil
}

var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func forward(before, after, bounds string, ranges bool) error {
	prefix := func(s string) string {
		if ranges && len(s) > 0 && (s[0] == '^' || s[0] == '~') {
			return s[:1]
		}
		return ""
	}
	bp, ap := prefix(before), prefix(after)
	if bp != ap {
		return errors.New("dependency range operator changed")
	}
	b, a := strings.TrimPrefix(before, bp), strings.TrimPrefix(after, ap)
	if !stableVersion.MatchString(b) || !stableVersion.MatchString(a) {
		return errors.New("only stable exact, caret or tilde versions are supported")
	}
	parse := func(s string) ([]uint64, error) {
		var out []uint64
		for _, part := range strings.Split(s, ".") {
			n, e := strconv.ParseUint(part, 10, 64)
			if e != nil {
				return nil, e
			}
			out = append(out, n)
		}
		return out, nil
	}
	bv, e := parse(b)
	if e != nil {
		return e
	}
	av, e := parse(a)
	if e != nil {
		return e
	}
	if bv[0] != av[0] || (bounds == "patch" && bv[1] != av[1]) || slices.Compare(av, bv) < 0 {
		return errors.New("version change exceeds the permitted forward update bound")
	}
	return nil
}

func packageName(path string) (string, error) {
	parts := strings.Split(path, "/node_modules/")
	for i, p := range parts {
		if i == 0 {
			if !strings.HasPrefix(p, "node_modules/") {
				return "", errors.New("unsupported package installation path")
			}
			p = strings.TrimPrefix(p, "node_modules/")
		}
		seg := strings.Split(p, "/")
		if len(seg) == 2 && strings.HasPrefix(seg[0], "@") {
			seg = []string{strings.TrimPrefix(seg[0], "@"), seg[1]}
		} else if len(seg) != 1 {
			return "", errors.New("unsupported package installation path")
		}
		for _, s := range seg {
			if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\\%?#:") {
				return "", errors.New("invalid package identity")
			}
		}
	}
	return strings.TrimPrefix(parts[len(parts)-1], "node_modules/"), nil
}
func packageRecord(path, name string, m map[string]any) (NPMPackage, error) {
	out := NPMPackage{Path: path, Name: name}
	out.Version, _ = m["version"].(string)
	out.Integrity, _ = m["integrity"].(string)
	if err := forward(out.Version, out.Version, "patch", false); err != nil {
		return out, err
	}
	if recorded, ok := m["name"]; ok && recorded != name {
		return out, errors.New("package alias or identity mismatch")
	}
	if m["link"] != nil {
		return out, errors.New("linked packages are unsupported")
	}
	resolved, ok := m["resolved"].(string)
	if !ok {
		return out, errors.New("package source is missing")
	}
	u, err := url.Parse(resolved)
	leaf := name[strings.LastIndex(name, "/")+1:]
	if err != nil || u.Scheme != "https" || u.Host != "registry.npmjs.org" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "/"+name+"/-/"+leaf+"-"+out.Version+".tgz" {
		return out, errors.New("package source is not its canonical public npm tarball")
	}
	digest, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(out.Integrity, "sha512-"))
	if err != nil || !strings.HasPrefix(out.Integrity, "sha512-") || len(digest) != 64 {
		return out, errors.New("package requires one SHA-512 integrity digest")
	}
	return out, nil
}
func behavior(m map[string]any) bool {
	for _, key := range []string{"hasInstallScript", "os", "cpu", "libc", "optional", "inBundle", "bundleDependencies", "bundledDependencies"} {
		if v, ok := m[key]; ok && v != false {
			return true
		}
	}
	if scripts, ok := m["scripts"].(map[string]any); ok {
		for _, key := range []string{"preinstall", "install", "postinstall", "prepare"} {
			if _, ok := scripts[key]; ok {
				return true
			}
		}
	}
	return false
}
func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
func sameKeys(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// object rejects duplicate keys at every depth: ordinary json.Unmarshal would
// discard a conflicting earlier value before the comparison could see it.
func object(raw []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 100 {
			return nil, errors.New("JSON nesting is too deep")
		}
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return t, nil
		}
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid object key")
				}
				if _, exists := m[k]; exists {
					return nil, fmt.Errorf("duplicate JSON key %q", k)
				}
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			if _, err = d.Token(); err != nil {
				return nil, err
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if _, err = d.Token(); err != nil {
				return nil, err
			}
			return a, nil
		default:
			return nil, errors.New("unexpected JSON delimiter")
		}
	}
	v, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON content")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("expected a JSON object")
	}
	return m, nil
}

// VerifyInstalled compares actual installed package manifests to the submitted
// lock. Reports must enumerate every installed package, including nested ones.
// The execution owner, not this parser, binds this observation to the exact
// input, immutable image and successful installation.
func VerifyInstalled(snapshot NPMSnapshot, expected []NPMPackage, installed map[string]json.RawMessage) error {
	lock, err := object(snapshot.Lock)
	if err != nil {
		return err
	}
	packages, ok := lock["packages"].(map[string]any)
	if !ok || len(packages) != len(expected)+1 || len(installed) != len(expected) {
		return errors.New("installed package inventory differs from the complete lock graph")
	}
	for _, p := range expected {
		raw, exists := installed[p.Path]
		if !exists {
			return fmt.Errorf("installed package %s is missing", p.Path)
		}
		actual, err := object(raw)
		if err != nil {
			return fmt.Errorf("installed %s: %w", p.Path, err)
		}
		if actual["name"] != p.Name || actual["version"] != p.Version {
			return fmt.Errorf("installed identity differs at %s", p.Path)
		}
		recorded, ok := packages[p.Path].(map[string]any)
		if !ok {
			return errors.New("lock package is missing")
		}
		for _, key := range []string{"dependencies", "optionalDependencies", "peerDependencies", "peerDependenciesMeta", "os", "cpu", "libc"} {
			if !reflect.DeepEqual(actual[key], recorded[key]) {
				return fmt.Errorf("installed %s %s differs from the lock", p.Path, key)
			}
		}
		if p.Changed && behavior(actual) {
			return fmt.Errorf("installed %s has lifecycle or platform behavior requiring a person", p.Path)
		}
	}
	return nil
}

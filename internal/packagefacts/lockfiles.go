package packagefacts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"golang.org/x/mod/modfile"
)

// Dependency is a direct dependency and the version its lockfile holds.
// Manifest is the file that declares it, relative to the tree.
type Dependency struct {
	Package
	Version  string `json:"version"`
	Manifest string `json:"manifest"`
}

// Direct lists the direct dependencies of every npm, uv, Go module, Bundler
// and Composer project in a tree, from the files those package managers
// write. Other package managers are not read. A lockfile that cannot be read
// is an error naming it: a dependency it would list cannot be judged.
func Direct(tree string) ([]Dependency, error) {
	var out []Dependency
	err := filepath.WalkDir(tree, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "node_modules", ".git", "vendor", ".venv":
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(tree, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		dir := path.Dir(rel)
		read := func() ([]byte, error) { return os.ReadFile(name) }
		var found []Dependency
		switch entry.Name() {
		case "package-lock.json":
			raw, err := read()
			if err == nil {
				found, err = npmDirect(raw, dir)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		case "uv.lock":
			raw, err := read()
			if err == nil {
				found, err = uvDirect(raw, path.Join(dir, "pyproject.toml"))
			}
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		case "go.mod":
			raw, err := read()
			if err == nil {
				found, err = goDirect(raw, rel)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		case "Gemfile.lock":
			raw, err := read()
			if err == nil {
				found, err = bundlerDirect(raw, path.Join(dir, "Gemfile"))
			}
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		case "composer.lock":
			raw, err := read()
			var manifest []byte
			if err == nil {
				manifest, err = os.ReadFile(filepath.Join(filepath.Dir(name), "composer.json"))
			}
			if err == nil {
				found, err = composerDirect(raw, manifest, path.Join(dir, "composer.json"))
			}
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		}
		out = append(out, found...)
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Manifest != out[j].Manifest {
			return out[i].Manifest < out[j].Manifest
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return Less(out[i].Version, out[j].Version)
	})
	return out, err
}

// npmDirect reads lockfileVersion 2 and 3: the root and each workspace
// folder are the entries outside node_modules; a dependency's version is the
// folder's own node_modules entry or the hoisted one.
func npmDirect(raw []byte, dir string) ([]Dependency, error) {
	type entry struct {
		Version              string            `json:"version"`
		Link                 bool              `json:"link"`
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
	}
	var lock struct {
		Packages map[string]entry `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, err
	}
	var out []Dependency
	for key, folder := range lock.Packages {
		if strings.Contains(key, "node_modules/") || strings.HasPrefix(key, "node_modules") {
			continue
		}
		names := map[string]bool{}
		for _, deps := range []map[string]string{folder.Dependencies, folder.DevDependencies, folder.OptionalDependencies, folder.PeerDependencies} {
			for name := range deps {
				names[name] = true
			}
		}
		for name := range names {
			at, ok := lock.Packages[path.Join(key, "node_modules", name)]
			if !ok {
				at, ok = lock.Packages["node_modules/"+name]
			}
			if !ok || at.Link || at.Version == "" {
				continue
			}
			out = append(out, Dependency{Package: Package{Type: NPM, Name: name}, Version: at.Version, Manifest: path.Join(dir, key, "package.json")})
		}
	}
	return out, nil
}

// uvDirect: the workspace members are the packages whose source is the
// project itself (editable or virtual); their dependencies, optional
// dependencies and dependency groups are the direct dependencies.
func uvDirect(raw []byte, manifest string) ([]Dependency, error) {
	type dep struct {
		Name    string `toml:"name"`
		Version string `toml:"version"`
	}
	var lock struct {
		Package []struct {
			Name     string           `toml:"name"`
			Version  string           `toml:"version"`
			Source   map[string]any   `toml:"source"`
			Deps     []dep            `toml:"dependencies"`
			Optional map[string][]dep `toml:"optional-dependencies"`
			Dev      map[string][]dep `toml:"dev-dependencies"`
		} `toml:"package"`
	}
	if err := toml.Unmarshal(raw, &lock); err != nil {
		return nil, err
	}
	versions := map[string][]string{}
	for _, p := range lock.Package {
		versions[normalizePyPI(p.Name)] = append(versions[normalizePyPI(p.Name)], p.Version)
	}
	// A universal lock may hold a direct dependency at several versions, one
	// per Python or platform marker; each is a direct version.
	direct := map[string]map[string]bool{}
	for _, p := range lock.Package {
		_, editable := p.Source["editable"]
		_, virtual := p.Source["virtual"]
		if !editable && !virtual {
			continue
		}
		all := append([]dep{}, p.Deps...)
		for _, group := range p.Optional {
			all = append(all, group...)
		}
		for _, group := range p.Dev {
			all = append(all, group...)
		}
		for _, d := range all {
			name := normalizePyPI(d.Name)
			candidates := versions[name]
			if d.Version != "" {
				candidates = []string{d.Version}
			}
			for _, v := range candidates {
				if direct[name] == nil {
					direct[name] = map[string]bool{}
				}
				direct[name][v] = true
			}
		}
	}
	// A workspace member is the project itself, not a dependency.
	for _, p := range lock.Package {
		if _, ok := p.Source["editable"]; ok {
			delete(direct, normalizePyPI(p.Name))
		}
		if _, ok := p.Source["virtual"]; ok {
			delete(direct, normalizePyPI(p.Name))
		}
	}
	var out []Dependency
	for name, versions := range direct {
		for v := range versions {
			if v != "" {
				out = append(out, Dependency{Package: Package{Type: PyPI, Name: name}, Version: v, Manifest: manifest})
			}
		}
	}
	return out, nil
}

func goDirect(raw []byte, manifest string) ([]Dependency, error) {
	f, err := modfile.ParseLax(manifest, raw, nil)
	if err != nil {
		return nil, err
	}
	var out []Dependency
	for _, r := range f.Require {
		if !r.Indirect {
			out = append(out, Dependency{Package: Package{Type: Go, Name: r.Mod.Path}, Version: r.Mod.Version, Manifest: manifest})
		}
	}
	return out, nil
}

// bundlerDirect: DEPENDENCIES names the Gemfile's gems; their versions are in
// the specs (four-space lines "name (version[-platform])").
func bundlerDirect(raw []byte, manifest string) ([]Dependency, error) {
	specs := map[string]string{}
	var direct []string
	section := ""
	scan := bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(make([]byte, 64*1024), 1<<20)
	for scan.Scan() {
		line := scan.Text()
		if line != "" && line[0] != ' ' {
			section = strings.TrimSpace(line)
			continue
		}
		switch {
		case section == "DEPENDENCIES" && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   "):
			name, _, _ := strings.Cut(strings.TrimSpace(line), " ")
			direct = append(direct, strings.TrimSuffix(name, "!"))
		case (section == "GEM" || section == "PATH" || section == "GIT") && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     "):
			name, version, ok := strings.Cut(strings.TrimSpace(line), " (")
			if !ok {
				continue
			}
			version = strings.TrimSuffix(version, ")")
			version, _, _ = strings.Cut(version, "-")
			if specs[name] == "" || Less(specs[name], version) {
				specs[name] = version
			}
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	var out []Dependency
	for _, name := range direct {
		if v := specs[name]; v != "" {
			out = append(out, Dependency{Package: Package{Type: Gem, Name: name}, Version: v, Manifest: manifest})
		}
	}
	return out, nil
}

// composerDirect: composer.json's require and require-dev name the direct
// packages (platform requirements such as php and ext-* have no vendor);
// composer.lock holds their versions.
func composerDirect(lockRaw, manifestRaw []byte, manifest string) ([]Dependency, error) {
	var lock struct {
		Packages    []struct{ Name, Version string } `json:"packages"`
		PackagesDev []struct{ Name, Version string } `json:"packages-dev"`
	}
	if err := json.Unmarshal(lockRaw, &lock); err != nil {
		return nil, err
	}
	var m struct {
		Require    map[string]string `json:"require"`
		RequireDev map[string]string `json:"require-dev"`
	}
	if err := json.Unmarshal(manifestRaw, &m); err != nil {
		return nil, fmt.Errorf("composer.json: %w", err)
	}
	versions := map[string]string{}
	for _, p := range append(lock.Packages, lock.PackagesDev...) {
		versions[strings.ToLower(p.Name)] = p.Version
	}
	var out []Dependency
	for _, req := range []map[string]string{m.Require, m.RequireDev} {
		for name := range req {
			if !strings.Contains(name, "/") {
				continue
			}
			if v := versions[strings.ToLower(name)]; v != "" {
				out = append(out, Dependency{Package: Package{Type: Composer, Name: strings.ToLower(name)}, Version: v, Manifest: manifest})
			}
		}
	}
	return out, nil
}

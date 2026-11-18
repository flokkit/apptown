//go:build integration

package stock

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/flokkit/apptown/internal/check"
	"github.com/flokkit/apptown/internal/execution"
)

// The direct pre-release listers ask the package manager itself, in the
// image AppTown runs them in, offline. Each case is a lockfile with a stable
// direct dependency that brings in a pre-release indirectly (Pocket Planet's
// vite -> rolldown -> optional wasm binding -> @emnapi/core 2.0.0-beta.2),
// and the same project with a pre-release the project declares itself. Only
// the second is a finding, named by its package and version so a pre-release
// already on main is the same finding there. A stub cannot prove what the
// tool lists, so these run the real tools; they need Docker and the images.
//
// A lockfile the package manager cannot read is exit 2 (not judged), never an
// empty, passing list.
//
// Kills: a lister that misses a direct dev dependency hidden by a committed
// .npmrc omit=dev, one that misses a direct dependency behind a platform
// marker (uv tree without --universal), one that lists indirect
// pre-releases, and one that calls a Go pseudo-version a pre-release.
func TestIntegrationDirectPreReleaseListersAskThePackageManager(t *testing.T) {
	scanners := stockScanners(t)
	for _, c := range []struct {
		name, scanner string
		files         map[string]string
		want          []string // fingerprints
		exit          int      // when not 0 or 1 by want
	}{
		{"npm: an indirect pre-release through optional and dev dependencies", "npm-direct-prereleases", npmProject(false), nil, 0},
		{"npm: a direct dev pre-release, though .npmrc omits dev", "npm-direct-prereleases", npmProject(true), []string{"prerelease|direct-prerelease|package.json:vitest@5.1.0-beta.1"}, 0},
		{"npm: a workspace's own direct pre-release", "npm-direct-prereleases", npmWorkspace(), []string{"prerelease|direct-prerelease|package.json:a>zod@4.0.0-beta.1"}, 0},
		{"uv: an indirect pre-release", "uv-direct-prereleases", uvProject(false), nil, 0},
		{"uv: a direct pre-release behind a platform marker", "uv-direct-prereleases", uvProject(true), []string{"prerelease|direct-prerelease|pyproject.toml:asgiref@3.9.0b1"}, 0},
		{"go: indirect pre-release, direct pseudo-version", "go-direct-prereleases", goProject(false), nil, 0},
		{"go: a direct pre-release", "go-direct-prereleases", goProject(true), []string{"prerelease|direct-prerelease|go.mod:github.com/a/b@v1.3.0-rc.1"}, 0},
		{"bundler: an indirect pre-release", "bundle-direct-prereleases", gemProject(false), nil, 0},
		{"bundler: a direct pre-release", "bundle-direct-prereleases", gemProject(true), []string{"prerelease|direct-prerelease|Gemfile:rails@8.1.0.beta1"}, 0},
		{"npm: no package-lock.json, nothing to list", "npm-direct-prereleases", map[string]string{"go.mod": "module x\n"}, nil, 0},
		{"npm: a lockfile npm cannot read is not judged", "npm-direct-prereleases", map[string]string{"package.json": `{"name":"t","version":"1.0.0"}`, "package-lock.json": "{broken"}, nil, 2},
		{"uv: a direct pre-release with an epoch", "uv-direct-prereleases", uvEpoch(), []string{"prerelease|direct-prerelease|pyproject.toml:asgiref@1!3.9.0b1"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, ok := scanners[c.scanner]
			if !ok {
				t.Fatalf("no scanner %s in scanners.yaml", c.scanner)
			}
			work := t.TempDir()
			for name, body := range c.files {
				path := filepath.Join(work, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			req := m.Request(work, m.Command)
			req.Name, req.Report, req.Timeout = execution.UniqueName("direct-prereleases"), m.Report, 5*time.Minute
			out := (execution.Runner{}).Run(t.Context(), execution.Docker, req)
			want := c.exit
			if len(c.want) > 0 {
				want = 1
			}
			if !out.Answered() || out.ExitCode != want {
				t.Fatalf("exit %d (%s), want %d: %v\n%s", out.ExitCode, out.Termination, want, out.RunErr, out.Output)
			}
			if want == 2 {
				return
			}
			findings, err := check.Normalize(m.Format, out.Report, m.Name, m.Kind)
			if err != nil {
				t.Fatalf("report: %v\n%s", err, out.Report)
			}
			var got []string
			for _, f := range findings {
				got = append(got, f.Fingerprint)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("findings %v, want %v\n%s", got, c.want, out.Output)
			}
		})
	}
}

func stockScanners(t *testing.T) map[string]check.Scanner {
	t.Helper()
	raw, err := FS.ReadFile("scanners.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Scanners []check.Scanner `yaml:"scanners"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]check.Scanner{}
	for _, s := range doc.Scanners {
		out[s.Name] = s
	}
	return out
}

// npmProject is Pocket Planet's chain in miniature: everything is a dev
// dependency and the wasm binding is optional. direct makes vitest, also a
// direct dev dependency, a pre-release and commits omit=dev.
func npmProject(direct bool) map[string]string {
	vitest, npmrc := "5.1.0", ""
	if direct {
		vitest, npmrc = "5.1.0-beta.1", "omit=dev\n"
	}
	files := map[string]string{
		"package.json": `{"name":"t","version":"1.0.0","devDependencies":{"vite":"^8.3.8","vitest":"` + vitest + `"}}`,
		"package-lock.json": `{"name":"t","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{
"":{"name":"t","version":"1.0.0","devDependencies":{"vite":"^8.3.8","vitest":"` + vitest + `"}},
"node_modules/vitest":{"version":"` + vitest + `","resolved":"https://registry.npmjs.org/vitest/-/vitest-` + vitest + `.tgz","integrity":"sha512-eee","dev":true},
"node_modules/vite":{"version":"8.3.8","resolved":"https://registry.npmjs.org/vite/-/vite-8.3.8.tgz","integrity":"sha512-aaa","dev":true,"dependencies":{"rolldown":"1.2.19"}},
"node_modules/rolldown":{"version":"1.2.19","resolved":"https://registry.npmjs.org/rolldown/-/rolldown-1.2.19.tgz","integrity":"sha512-bbb","dev":true,"optionalDependencies":{"@rolldown/binding-wasm32-wasi":"1.2.19"}},
"node_modules/@rolldown/binding-wasm32-wasi":{"version":"1.2.19","resolved":"https://registry.npmjs.org/@rolldown/binding-wasm32-wasi/-/binding-wasm32-wasi-1.2.19.tgz","integrity":"sha512-ccc","dev":true,"optional":true,"dependencies":{"@emnapi/core":"2.0.0-beta.2"}},
"node_modules/@emnapi/core":{"version":"2.0.0-beta.2","resolved":"https://registry.npmjs.org/@emnapi/core/-/core-2.0.0-beta.2.tgz","integrity":"sha512-ddd","dev":true,"optional":true}
}}`,
	}
	if npmrc != "" {
		files[".npmrc"] = npmrc
	}
	return files
}

func npmWorkspace() map[string]string {
	return map[string]string{
		"package.json":            `{"name":"root","version":"1.0.0","workspaces":["packages/*"]}`,
		"packages/a/package.json": `{"name":"a","version":"1.0.0","dependencies":{"zod":"4.0.0-beta.1"}}`,
		"package-lock.json": `{"name":"root","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{
"":{"name":"root","version":"1.0.0","workspaces":["packages/*"]},
"packages/a":{"name":"a","version":"1.0.0","dependencies":{"zod":"4.0.0-beta.1"}},
"node_modules/a":{"resolved":"packages/a","link":true},
"node_modules/zod":{"version":"4.0.0-beta.1","resolved":"https://registry.npmjs.org/zod/-/zod-4.0.0-beta.1.tgz","integrity":"sha512-zzz"}
}}`,
	}
}

// uvProject: django (stable, direct) requires asgiref 3.9.0b1. direct
// declares asgiref on the project itself, only on Windows.
func uvProject(direct bool) map[string]string {
	hash := strings.Repeat("a", 64)
	deps, lockDeps, djangoDeps := `"django>=5"`, `[{ name = "django" }]`, `[{ name = "asgiref" }]`
	if direct {
		deps = `"django>=5", "asgiref==3.9.0b1; sys_platform == 'win32'"`
		lockDeps = `[{ name = "asgiref", marker = "sys_platform == 'win32'" }, { name = "django" }]`
		djangoDeps = `[]`
	}
	return map[string]string{
		"pyproject.toml": "[project]\nname = \"app\"\nversion = \"0.1.0\"\nrequires-python = \">=3.12\"\ndependencies = [" + deps + "]\n",
		"uv.lock": `version = 1
requires-python = ">=3.12"

[[package]]
name = "app"
version = "0.1.0"
source = { virtual = "." }
dependencies = ` + lockDeps + `

[[package]]
name = "asgiref"
version = "3.9.0b1"
source = { registry = "https://pypi.org/simple" }
sdist = { url = "https://x/asgiref.tar.gz", hash = "sha256:` + hash + `" }

[[package]]
name = "django"
version = "5.2.0"
source = { registry = "https://pypi.org/simple" }
dependencies = ` + djangoDeps + `
sdist = { url = "https://x/django.tar.gz", hash = "sha256:` + hash + `" }
`,
	}
}

// uvEpoch: a direct beta whose version starts with a PEP 440 epoch.
func uvEpoch() map[string]string {
	files := uvProject(true)
	for name, body := range files {
		files[name] = strings.ReplaceAll(body, "3.9.0b1", "1!3.9.0b1")
	}
	return files
}

func goProject(direct bool) map[string]string {
	b := "v1.2.0"
	if direct {
		b = "v1.3.0-rc.1"
	}
	return map[string]string{"go.mod": `module example.com/app

go 1.24

require (
	github.com/a/b ` + b + `
	github.com/e/f v0.0.0-20240101000000-abcdefabcdef
)

require github.com/c/d v1.5.0-beta.1 // indirect
`}
}

func gemProject(direct bool) map[string]string {
	rails := "8.0.1"
	if direct {
		rails = "8.1.0.beta1"
	}
	return map[string]string{
		"Gemfile": "source \"https://rubygems.org\"\ngem \"rails\", \"" + rails + "\"\n",
		"Gemfile.lock": `GEM
  remote: https://rubygems.org/
  specs:
    actionpack (` + rails + `)
      rack (>= 2.2.4)
    rack (3.2.0.beta1)
    rails (` + rails + `)
      actionpack (= ` + rails + `)

PLATFORMS
  ruby

DEPENDENCIES
  rails (= ` + rails + `)

BUNDLED WITH
   2.5.0
`,
	}
}

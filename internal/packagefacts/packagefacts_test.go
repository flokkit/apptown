package packagefacts

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLinesStabilityAndPurls(t *testing.T) {
	t.Parallel()
	for v, want := range map[string]string{"18.3.1": "18", "v2.4.0": "2", "0.12.3": "0.12", "4.2.16": "4", "v1.2.3+incompatible": "1", "dev-main": ""} {
		if got := Line(v); got != want {
			t.Errorf("Line(%q) = %q, want %q", v, got, want)
		}
	}
	if !LineLess("0.9", "0.10") || !LineLess("0.10", "1") || LineLess("2", "2") {
		t.Error("line order")
	}
	for _, c := range []struct {
		typ, v string
		stable bool
	}{
		{NPM, "19.0.0", true}, {NPM, "19.0.0-rc.1", false},
		{PyPI, "5.0", true}, {PyPI, "5.0rc1", false}, {PyPI, "5.0.post1", true}, {PyPI, "5.1.dev0", false}, {PyPI, "5.0b2", false},
		{Gem, "7.1.0", true}, {Gem, "7.1.0.beta1", false},
		{Go, "v2.0.0", true}, {Go, "v2.0.0-beta.1", false},
		{Composer, "v11.0.0", true}, {Composer, "11.0.0-RC1", false}, {Composer, "dev-master", false}, {Composer, "11.x-dev", false},
	} {
		if got := isStable(c.typ, c.v); got != c.stable {
			t.Errorf("stable(%s, %s) = %t", c.typ, c.v, got)
		}
	}
	p, v, err := ParsePurl("pkg:npm/%40angular/core@17.3.0")
	if err != nil || p != (Package{Type: NPM, Name: "@angular/core"}) || v != "17.3.0" || p.purl() != "pkg:npm/%40angular/core" {
		t.Fatalf("%+v %s %v %s", p, v, err, p.purl())
	}
	if _, _, err := ParsePurl("pkg:cargo/serde@1.0.0"); err == nil {
		t.Error("an unread ecosystem was accepted")
	}
	if (Package{Type: Go, Name: "github.com/x/y/v3"}).Identity() != (Package{Type: Go, Name: "github.com/x/y"}).Identity() ||
		(Package{Type: Go, Name: "gopkg.in/yaml.v3"}).Identity() != (Package{Type: Go, Name: "gopkg.in/yaml.v2"}).Identity() ||
		(Package{Type: PyPI, Name: "Zope.Interface"}).Identity() != (Package{Type: PyPI, Name: "zope-interface"}).Identity() {
		t.Error("identity across majors")
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDirectReadsEachLockfile(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	write(t, tree, "web/package-lock.json", `{"lockfileVersion":3,"packages":{
		"":{"dependencies":{"react":"^18"},"devDependencies":{"vite":"^5"},"workspaces":["packages/ui"]},
		"packages/ui":{"dependencies":{"lodash":"^3"}},
		"node_modules/react":{"version":"18.3.1"},
		"node_modules/vite":{"version":"5.4.0"},
		"node_modules/lodash":{"version":"4.17.21"},
		"packages/ui/node_modules/lodash":{"version":"3.10.1"},
		"node_modules/ui":{"resolved":"packages/ui","link":true},
		"node_modules/scheduler":{"version":"0.23.2"}}}`)
	write(t, tree, "web/node_modules/x/package-lock.json", `not read`)
	write(t, tree, "api/uv.lock", `version = 1
[[package]]
name = "api"
version = "0.1.0"
source = { virtual = "." }
dependencies = [{ name = "django" }]
[package.dev-dependencies]
dev = [{ name = "pytest" }]

[[package]]
name = "django"
version = "4.2.16"
source = { registry = "https://pypi.org/simple" }
dependencies = [{ name = "asgiref" }]

[[package]]
name = "asgiref"
version = "3.8.1"
source = { registry = "https://pypi.org/simple" }

[[package]]
name = "pytest"
version = "8.3.3"
source = { registry = "https://pypi.org/simple" }
`)
	write(t, tree, "svc/go.mod", "module example.com/svc\n\ngo 1.27\n\nrequire (\n\tgithub.com/gorilla/mux v1.8.0\n\tgithub.com/x/y/v2 v2.1.0\n\tgolang.org/x/text v0.20.0 // indirect\n)\n")
	write(t, tree, "rb/Gemfile.lock", "GEM\n  remote: https://rubygems.org/\n  specs:\n    nokogiri (1.16.0-x86_64-linux)\n      racc (~> 1.4)\n    racc (1.8.1)\n    rails (7.1.3)\n\nPLATFORMS\n  x86_64-linux\n\nDEPENDENCIES\n  nokogiri\n  rails (~> 7.1)\n\nBUNDLED WITH\n   2.5.0\n")
	write(t, tree, "php/composer.json", `{"require":{"php":"^8.3","ext-json":"*","laravel/framework":"^10.0"},"require-dev":{"phpunit/phpunit":"^10"}}`)
	write(t, tree, "php/composer.lock", `{"packages":[{"name":"laravel/framework","version":"v10.48.4"},{"name":"symfony/console","version":"v6.4.0"}],"packages-dev":[{"name":"phpunit/phpunit","version":"10.5.0"}]}`)
	got, err := Direct(tree)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, d := range got {
		lines = append(lines, d.Manifest+" "+d.Type+" "+d.Name+" "+d.Version)
	}
	want := []string{
		"api/pyproject.toml pypi django 4.2.16",
		"api/pyproject.toml pypi pytest 8.3.3",
		"php/composer.json composer laravel/framework v10.48.4",
		"php/composer.json composer phpunit/phpunit 10.5.0",
		"rb/Gemfile gem nokogiri 1.16.0",
		"rb/Gemfile gem rails 7.1.3",
		"svc/go.mod golang github.com/gorilla/mux v1.8.0",
		"svc/go.mod golang github.com/x/y/v2 v2.1.0",
		"web/package.json npm react 18.3.1",
		"web/package.json npm vite 5.4.0",
		"web/packages/ui/package.json npm lodash 3.10.1",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("got\n%s", strings.Join(lines, "\n"))
	}
	write(t, tree, "bad/package-lock.json", "{")
	if _, err := Direct(tree); err == nil || !strings.Contains(err.Error(), "bad/package-lock.json") {
		t.Fatalf("an unreadable lockfile was not named: %v", err)
	}
}

func TestRegistriesSummarizeTheNewestLine(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/npm/react", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"time":{"created":"2013-01-01T00:00:00Z","18.3.1":"2024-04-26T00:00:00Z","19.0.0-rc.1":"2024-11-01T00:00:00Z","19.0.0":"2024-12-05T00:00:00Z","19.1.0":"2025-03-28T00:00:00Z","19.2.0":"2025-10-01T00:00:00Z"},
			"versions":{"18.3.1":{"deprecated":"React 18 is deprecated"},"19.0.0-rc.1":{},"19.0.0":{},"19.1.0":{},"19.2.0":{"deprecated":false}}}`))
	})
	mux.HandleFunc("/pypi/pypi/django/json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"releases":{"4.2.16":[{"upload_time_iso_8601":"2024-09-03T00:00:00Z"}],"5.0":[{"upload_time_iso_8601":"2023-12-04T00:00:00Z"}],"5.0.1":[{"upload_time_iso_8601":"2024-01-02T00:00:00Z","yanked":true}],"5.1rc1":[{"upload_time_iso_8601":"2024-07-01T00:00:00Z"}],"5.0.2":[{"upload_time_iso_8601":"2024-02-06T00:00:00Z"}],"5.0.3":[]}}`))
	})
	mux.HandleFunc("/gems/api/v1/versions/rails.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"number":"8.0.1","created_at":"2024-12-13T00:00:00Z"},{"number":"8.0.0","created_at":"2024-11-07T00:00:00Z"},{"number":"8.0.0","created_at":"2024-11-07T00:00:00Z"},{"number":"8.0.0.rc1","created_at":"2024-10-01T00:00:00Z","prerelease":true},{"number":"7.1.3","created_at":"2024-01-16T00:00:00Z"}]`))
	})
	mux.HandleFunc("/packagist/p2/laravel/framework.json", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"minified":"composer/2.0","packages":{"laravel/framework":[{"name":"laravel/framework","version":"v11.1.0","time":"2024-03-26T00:00:00+00:00"},{"version":"v11.0.0","time":"2024-03-12T00:00:00+00:00"},{"version":"v11.0.0-RC1","time":"2024-03-01T00:00:00+00:00"},{"version":"v10.48.4","time":"2024-03-21T00:00:00+00:00"}]}}`))
	})
	mux.HandleFunc("/go/github.com/x/y/@v/list", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("v1.0.0\nv1.2.0\n")) })
	mux.HandleFunc("/go/github.com/x/y/v2/@v/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("v2.0.0\nv2.0.1\nv2.1.0-beta.1\n"))
	})
	mux.HandleFunc("/go/github.com/x/y/v2/@v/v2.0.0.info", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Version":"v2.0.0","Time":"2025-01-02T00:00:00Z"}`))
	})
	mux.HandleFunc("/go/github.com/x/y/v3/@v/list", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "gone", http.StatusGone) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	c := Client{HTTP: server.Client(), NPM: server.URL + "/npm", PyPI: server.URL + "/pypi", RubyGems: server.URL + "/gems", Packagist: server.URL + "/packagist", GoProxy: server.URL + "/go"}
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	for _, c2 := range []struct {
		p       Package
		current string
		want    Lines
	}{
		{Package{NPM, "react"}, "18.3.1", Lines{Newest: "19", First: Release{"19.0.0", day("2024-12-05")}, Since: 2, Deprecated: "React 18 is deprecated"}},
		{Package{PyPI, "django"}, "4.2.16", Lines{Newest: "5", First: Release{"5.0", day("2023-12-04")}, Since: 1}},
		{Package{Gem, "rails"}, "7.1.3", Lines{Newest: "8", First: Release{"8.0.0", day("2024-11-07")}, Since: 1}},
		{Package{Composer, "laravel/framework"}, "v10.48.4", Lines{Newest: "11", First: Release{"v11.0.0", day("2024-03-12")}, Since: 1}},
		{Package{Go, "github.com/x/y"}, "v1.2.0", Lines{Newest: "2", First: Release{"v2.0.0", day("2025-01-02")}, Since: 1}},
	} {
		got, err := c.Lines(t.Context(), c2.p, c2.current, Line)
		if err != nil || !reflect.DeepEqual(got, c2.want) {
			t.Errorf("%s: %+v %v, want %+v", c2.p.Name, got, err, c2.want)
		}
	}
	if _, err := c.Lines(t.Context(), Package{NPM, "missing"}, "1.0.0", Line); err == nil {
		t.Error("an absent package answered")
	}
}

func TestEndOfLifeMapsPurlsToCycles(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/identifiers/purl/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":[{"identifier":"pkg:pypi/django","product":{"name":"django"}},{"identifier":"pkg:npm/%40angular/core","product":{"name":"angular"}}]}`))
	})
	mux.HandleFunc("/api/v1/products/django/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"releases":[{"name":"5.2","isEol":false,"eolFrom":"2028-04-30"},{"name":"5.1","isEol":true,"eolFrom":"2025-12-03"},{"name":"6.0","isEol":false,"eolFrom":null}]}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	c := Client{HTTP: server.Client(), EndOfLife: server.URL}
	got, err := c.Support(t.Context(), Package{PyPI, "Django"})
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	want := Support{Tracked: true, Product: "django", Cycles: []Cycle{{Name: "5.2", Ends: day("2028-04-30")}, {Name: "5.1", Ends: day("2025-12-03"), Ended: true}, {Name: "6.0"}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := c.Support(t.Context(), Package{NPM, "left-pad"}); err != nil || got.Tracked {
		t.Fatalf("untracked: %+v %v", got, err)
	}
	if got, err := c.Support(t.Context(), Package{NPM, "@angular/core"}); err == nil || got.Tracked {
		t.Fatalf("an unreadable product answered: %+v %v", got, err)
	}
}

// A version's release cycle is the longest numbered cycle name that is a
// whole-number prefix of it; an image is looked up by its Docker purl; the
// narrowest line is x.y; endoflife.date's cycles give no release count.
func TestReleaseCyclesOfVersionsAndImages(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/identifiers/purl/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":[{"identifier":"pkg:docker/library/python","product":{"name":"python"}}]}`))
	})
	mux.HandleFunc("/api/v1/products/python/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"releases":[{"name":"3.13","releaseDate":"2024-10-07","isEol":false,"eolFrom":"2029-10-31"},{"name":"3.12","releaseDate":"2023-10-02","isEol":false,"eolFrom":"2028-10-31"},{"name":"3.1","releaseDate":"2009-06-27","isEol":true,"eolFrom":"2012-04-09"}]}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	c := Client{HTTP: server.Client(), EndOfLife: server.URL}
	p, version, err := ParsePurl("pkg:docker/library/python@3.12.8")
	if err != nil || p != (Package{Docker, "library/python"}) || version != "3.12.8" {
		t.Fatalf("%+v %s %v", p, version, err)
	}
	s, err := c.Support(t.Context(), p)
	if err != nil || !s.Tracked || s.Product != "python" {
		t.Fatalf("%+v %v", s, err)
	}
	for v, want := range map[string]string{"3.12.8": "3.12", "3.13.0": "3.13", "3.1.5": "3.1", "3.10.2": "", "3": ""} {
		got, ok := s.CycleOf(v)
		if ok != (want != "") || got.Name != want {
			t.Errorf("%s: %q %t, want %q", v, got.Name, ok, want)
		}
	}
	if c, _ := s.CycleOf("3.12.8"); !c.Released.Equal(time.Date(2023, 10, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("released %v", c.Released)
	}
	node := Support{Tracked: true, Product: "nodejs", Cycles: []Cycle{{Name: "24"}, {Name: "2"}}}
	if got, ok := node.CycleOf("24.22.2"); !ok || got.Name != "24" {
		t.Errorf("node 24.22.2 on %q", got.Name)
	}
	for v, want := range map[string]string{"8.3.35": "8.3", "24": "24", "1.26.1": "1.26", "v2.10.2": "2.10", "nonroot": ""} {
		if got := Narrow(v); got != want {
			t.Errorf("Narrow(%s) = %q, want %q", v, got, want)
		}
	}
	lines, err := CycleLines(s)
	if err != nil || lines.Newest != "3.13" || lines.First.Version != "3.13" || !lines.First.Published.Equal(time.Date(2024, 10, 7, 0, 0, 0, 0, time.UTC)) || lines.Since != -1 {
		t.Fatalf("%+v %v", lines, err)
	}
}

// A registry's releases group by the caller's line: by release cycle,
// Django 5.1 → 5.2 is a move to another line.
func TestRegistryLinesGroupByTheGivenLine(t *testing.T) {
	t.Parallel()
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	releases := []Release{{"5.1.4", day("2024-12-04")}, {"5.2.0", day("2025-04-02")}, {"5.2.1", day("2025-05-07")}, {"5.2.2", day("2025-06-04")}}
	cycles := Support{Tracked: true, Cycles: []Cycle{{Name: "5.1"}, {Name: "5.2"}}}
	byCycle := func(v string) string { c, _ := cycles.CycleOf(v); return c.Name }
	got, err := summarize(PyPI, releases, nil, "5.1.4", byCycle)
	if err != nil || got.Newest != "5.2" || got.First.Version != "5.2.0" || got.Since != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := summarize(PyPI, releases, nil, "5.1.4", Line); got.Newest != "5" || got.First.Version != "5.1.4" {
		t.Fatalf("semver: %+v", got)
	}
}

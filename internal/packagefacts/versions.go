// Package packagefacts reads facts about application packages: the direct
// dependencies a committed tree locks, the stable major lines a package
// registry has published and when, and endoflife.date's support dates. It
// decides nothing; callers apply their own rules to these facts. A package is
// named as a purl type and name (npm, pypi, golang, gem, composer), the
// identifiers endoflife.date uses.
package packagefacts

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The purl types this package reads.
const (
	NPM      = "npm"
	PyPI     = "pypi"
	Go       = "golang"
	Gem      = "gem"
	Composer = "composer"
	// Docker is a Docker Hub image (pkg:docker/library/php): endoflife.date
	// maps images by it. AppTown reads no registry for it here; its lines
	// are endoflife.date's cycles.
	Docker = "docker"
)

// Package is one package in one ecosystem.
type Package struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// purl is the package URL without a version (pkg:npm/%40scope/name).
func (p Package) purl() string {
	parts := strings.Split(p.Name, "/")
	for i, s := range parts {
		parts[i] = strings.ReplaceAll(url.PathEscape(s), "@", "%40")
	}
	return "pkg:" + p.Type + "/" + strings.Join(parts, "/")
}

// Identity names the package across its majors: a Go module's major path
// suffix (/v2, gopkg.in's .v2) is a major of the same package, and PyPI
// names compare normalized.
func (p Package) Identity() string {
	switch p.Type {
	case Go:
		return p.Type + "/" + goBasePath(p.Name)
	case PyPI:
		return p.Type + "/" + normalizePyPI(p.Name)
	}
	return p.Type + "/" + p.Name
}

// ParsePurl reads pkg:<type>/<name>@<version>.
func ParsePurl(s string) (Package, string, error) {
	rest, ok := strings.CutPrefix(s, "pkg:")
	if !ok {
		return Package{}, "", fmt.Errorf("%q is not a package URL (pkg:npm/name@1.2.3)", s)
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	typ, path, ok := strings.Cut(rest, "/")
	if !ok || path == "" {
		return Package{}, "", fmt.Errorf("%q names no package", s)
	}
	at := strings.LastIndex(path, "@")
	if at <= 0 {
		return Package{}, "", fmt.Errorf("%q names no version", s)
	}
	name, version := path[:at], path[at+1:]
	name, err := url.PathUnescape(name)
	if err != nil {
		return Package{}, "", fmt.Errorf("%q: %w", s, err)
	}
	if version, err = url.PathUnescape(version); err != nil {
		return Package{}, "", fmt.Errorf("%q: %w", s, err)
	}
	switch typ = strings.ToLower(typ); typ {
	case NPM, PyPI, Go, Gem, Composer, Docker:
	default:
		return Package{}, "", fmt.Errorf("%q: AppTown reads npm, pypi, golang, gem, composer and docker packages", s)
	}
	return Package{Type: typ, Name: name}, version, nil
}

var goMajorSuffix = regexp.MustCompile(`/v[0-9]+$`)
var gopkgSuffix = regexp.MustCompile(`^(gopkg\.in/.+)\.v[0-9]+$`)

func goBasePath(path string) string {
	if m := gopkgSuffix.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return goMajorSuffix.ReplaceAllString(path, "")
}

var pypiSeparators = regexp.MustCompile(`[-_.]+`)

func normalizePyPI(name string) string {
	return pypiSeparators.ReplaceAllString(strings.ToLower(name), "-")
}

var leadingNumbers = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*`)

// numbers are a version's leading dotted numbers (v1.2.3-rc.1 → 1 2 3).
func numbers(version string) []int {
	v := strings.TrimPrefix(strings.TrimPrefix(version, "v"), "V")
	m := leadingNumbers.FindString(v)
	if m == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(m, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

var pypiPre = regexp.MustCompile(`(?i)(a|b|c|rc|alpha|beta|pre|preview|dev)[0-9]*`)
var composerPre = regexp.MustCompile(`(?i)(dev|alpha|beta|rc|[-.]a[0-9]|[-.]b[0-9])`)

// isStable reports whether a version is a release, not a pre-release, in its
// ecosystem's own spelling.
func isStable(typ, version string) bool {
	if numbers(version) == nil || strings.Contains(version, "!") {
		return false
	}
	v := strings.TrimPrefix(version, "v")
	rest := v[len(leadingNumbers.FindString(v)):]
	switch typ {
	case NPM:
		return rest == "" || strings.HasPrefix(rest, "+")
	case Go:
		return rest == "" || rest == "+incompatible"
	case PyPI:
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, "."), "-")
		return !pypiPre.MatchString(rest)
	case Gem:
		return rest == ""
	case Composer:
		return !composerPre.MatchString(rest)
	}
	return false
}

// Line is the version line a major move leaves: the major, or 0.minor for a
// 0.x version (a 0.x minor is a major step). Empty when unreadable.
func Line(version string) string {
	n := numbers(version)
	switch {
	case len(n) == 0:
		return ""
	case n[0] == 0 && len(n) > 1:
		return fmt.Sprintf("0.%d", n[1])
	}
	return strconv.Itoa(n[0])
}

// Narrow is the narrowest line a version can be on: its first two numbers
// (8.3 for 8.3.35), or fewer when it has fewer. Empty when unreadable.
func Narrow(version string) string {
	n := numbers(version)
	if len(n) > 2 {
		n = n[:2]
	}
	parts := make([]string, len(n))
	for i, x := range n {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ".")
}

// LineLess orders two lines (0.9 < 0.10 < 1 < 2).
func LineLess(a, b string) bool {
	x, y := numbers(a), numbers(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}

// Less orders two versions by their leading numbers.
func Less(a, b string) bool {
	x, y := numbers(a), numbers(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p < q
		}
	}
	return false
}

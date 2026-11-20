package packagefacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/module"
)

// Client reads the public package registries and endoflife.date. Empty base
// URLs are the public services; tests point them elsewhere.
type Client struct {
	HTTP                                               *http.Client
	NPM, PyPI, RubyGems, Packagist, GoProxy, EndOfLife string
}

// Release is one stable release and when the registry says it was published.
type Release struct {
	Version   string    `json:"version"`
	Published time.Time `json:"published"`
}

// Lines summarizes a package's stable lines for a major decision.
type Lines struct {
	// Newest is the highest line with a stable release.
	Newest string `json:"newest"`
	// First is the newest line's first stable release; Since counts the
	// stable releases published on that line after it.
	First Release `json:"first"`
	Since int     `json:"since"`
	// Deprecated is the registry's deprecation message on the newest stable
	// release of the current line (npm only), empty when none.
	Deprecated string `json:"deprecated,omitempty"`
}

// maxBody bounds a registry document: npm's full documents for the largest
// packages are tens of megabytes.
const maxBody = 128 << 20

func (c Client) get(ctx context.Context, rawURL string, into any) (found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	if s, ok := into.(*string); ok {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		*s = string(raw)
		return true, err
	}
	return true, json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(into)
}

func base(configured, public string) string {
	if configured != "" {
		return strings.TrimSuffix(configured, "/")
	}
	return public
}

// Lines reads the package's stable releases from its registry and
// summarizes them relative to the version in use.
func (c Client) Lines(ctx context.Context, p Package, current string) (Lines, error) {
	var releases []Release
	deprecated := map[string]string{}
	var err error
	switch p.Type {
	case NPM:
		releases, deprecated, err = c.npm(ctx, p.Name)
	case PyPI:
		releases, err = c.pypi(ctx, p.Name)
	case Gem:
		releases, err = c.gems(ctx, p.Name)
	case Composer:
		releases, err = c.packagist(ctx, p.Name)
	case Go:
		releases, err = c.goproxy(ctx, p.Name, current)
	default:
		err = fmt.Errorf("AppTown does not read %s registries", p.Type)
	}
	if err != nil {
		return Lines{}, err
	}
	return summarize(p.Type, releases, deprecated, current)
}

func summarize(typ string, releases []Release, deprecated map[string]string, current string) (Lines, error) {
	var stable []Release
	for _, r := range releases {
		if isStable(typ, r.Version) && Line(r.Version) != "" {
			r.Published = r.Published.UTC()
			stable = append(stable, r)
		}
	}
	if len(stable) == 0 {
		return Lines{}, errors.New("the registry lists no stable release")
	}
	sort.SliceStable(stable, func(i, j int) bool { return Less(stable[i].Version, stable[j].Version) })
	out := Lines{Newest: Line(stable[len(stable)-1].Version)}
	currentLine, newestOnCurrent := Line(current), ""
	for _, r := range stable {
		if Line(r.Version) == out.Newest {
			if out.First.Version == "" {
				out.First = r
			} else {
				out.Since++
			}
		}
		if Line(r.Version) == currentLine {
			newestOnCurrent = r.Version
		}
	}
	out.Deprecated = deprecated[newestOnCurrent]
	return out, nil
}

func (c Client) npm(ctx context.Context, name string) ([]Release, map[string]string, error) {
	var doc struct {
		Time     map[string]string `json:"time"`
		Versions map[string]struct {
			Deprecated json.RawMessage `json:"deprecated"`
		} `json:"versions"`
	}
	found, err := c.get(ctx, base(c.NPM, "https://registry.npmjs.org")+"/"+url.PathEscape(name), &doc)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, fmt.Errorf("npm has no package %s", name)
	}
	var out []Release
	deprecated := map[string]string{}
	for v, d := range doc.Versions {
		t, _ := time.Parse(time.RFC3339, doc.Time[v])
		out = append(out, Release{Version: v, Published: t})
		var msg string
		if json.Unmarshal(d.Deprecated, &msg) == nil && msg != "" {
			deprecated[v] = msg
		}
	}
	return out, deprecated, nil
}

func (c Client) pypi(ctx context.Context, name string) ([]Release, error) {
	var doc struct {
		Releases map[string][]struct {
			Uploaded string `json:"upload_time_iso_8601"`
			Yanked   bool   `json:"yanked"`
		} `json:"releases"`
	}
	found, err := c.get(ctx, base(c.PyPI, "https://pypi.org")+"/pypi/"+url.PathEscape(name)+"/json", &doc)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("PyPI has no project %s", name)
	}
	var out []Release
	for v, files := range doc.Releases {
		var first time.Time
		yanked := len(files) > 0
		for _, f := range files {
			yanked = yanked && f.Yanked
			if t, err := time.Parse(time.RFC3339, f.Uploaded); err == nil && (first.IsZero() || t.Before(first)) {
				first = t
			}
		}
		// A release with no files was never installable.
		if len(files) > 0 && !yanked {
			out = append(out, Release{Version: v, Published: first})
		}
	}
	return out, nil
}

func (c Client) gems(ctx context.Context, name string) ([]Release, error) {
	var doc []struct {
		Number     string `json:"number"`
		Created    string `json:"created_at"`
		Prerelease bool   `json:"prerelease"`
	}
	found, err := c.get(ctx, base(c.RubyGems, "https://rubygems.org")+"/api/v1/versions/"+url.PathEscape(name)+".json", &doc)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("RubyGems has no gem %s", name)
	}
	seen := map[string]bool{}
	var out []Release
	for _, g := range doc {
		// Platform builds repeat a number; the first published counts.
		if g.Prerelease || seen[g.Number] {
			continue
		}
		seen[g.Number] = true
		t, _ := time.Parse(time.RFC3339, g.Created)
		out = append(out, Release{Version: g.Number, Published: t})
	}
	return out, nil
}

func (c Client) packagist(ctx context.Context, name string) ([]Release, error) {
	// Composer 2 metadata; every entry carries its own version and time.
	var doc struct {
		Packages map[string][]struct {
			Version string `json:"version"`
			Time    string `json:"time"`
		} `json:"packages"`
	}
	found, err := c.get(ctx, base(c.Packagist, "https://repo.packagist.org")+"/p2/"+name+".json", &doc)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("Packagist has no package %s", name)
	}
	var out []Release
	for _, p := range doc.Packages[name] {
		t, _ := time.Parse(time.RFC3339, p.Time)
		out = append(out, Release{Version: p.Version, Published: t})
	}
	return out, nil
}

// goproxy lists the module's versions on its own path and on each higher
// major path (/v2, /v3 …) until one is absent, and reads the publish time of
// the first release on the newest line only: that is the time a decision
// needs, and the proxy answers one version per request.
func (c Client) goproxy(ctx context.Context, path, current string) ([]Release, error) {
	proxy := base(c.GoProxy, "https://proxy.golang.org")
	list := func(modPath string) ([]string, bool, error) {
		escaped, err := module.EscapePath(modPath)
		if err != nil {
			return nil, false, err
		}
		var body string
		found, err := c.get(ctx, proxy+"/"+escaped+"/@v/list", &body)
		if err != nil || !found {
			return nil, found, err
		}
		return strings.Fields(body), true, nil
	}
	root := goBasePath(path)
	gopkg := strings.HasPrefix(root, "gopkg.in/")
	at := func(major int) string {
		switch {
		case gopkg:
			return root + ".v" + strconv.Itoa(major)
		case major < 2:
			return root
		}
		return root + "/v" + strconv.Itoa(major)
	}
	type version struct{ path, v string }
	var all []version
	major := 0
	if n := numbers(current); len(n) > 0 {
		major = n[0]
	}
	vs, found, err := list(at(major))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("the Go module proxy has no module %s", at(major))
	}
	for _, v := range vs {
		all = append(all, version{at(major), v})
	}
	for next := max(major+1, 2); ; next++ {
		vs, found, err := list(at(next))
		if err != nil {
			return nil, err
		}
		if !found || len(vs) == 0 {
			break
		}
		for _, v := range vs {
			all = append(all, version{at(next), v})
		}
	}
	var out []Release
	newest, first := "", version{}
	for _, v := range all {
		if !isStable(Go, v.v) || strings.HasSuffix(v.v, "+incompatible") {
			continue
		}
		out = append(out, Release{Version: v.v})
		if newest == "" || LineLess(newest, Line(v.v)) {
			newest, first = Line(v.v), v
		} else if Line(v.v) == newest && Less(v.v, first.v) {
			first = v
		}
	}
	if first.v == "" {
		return out, nil
	}
	escaped, err := module.EscapePath(first.path)
	if err != nil {
		return nil, err
	}
	var info struct{ Time time.Time }
	if found, err := c.get(ctx, proxy+"/"+escaped+"/@v/"+first.v+".info", &info); err != nil || !found {
		return nil, fmt.Errorf("the Go module proxy has no time for %s@%s: %v", first.path, first.v, err)
	}
	for i := range out {
		if out[i].Version == first.v {
			out[i].Published = info.Time
		}
	}
	return out, nil
}

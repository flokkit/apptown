package check

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"

	"golang.org/x/net/html"
)

// Fetcher gets a URL's status and body.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (int, string, error)
}

// FetcherFunc adapts a function to Fetcher.
type FetcherFunc func(ctx context.Context, url string) (int, string, error)

func (f FetcherFunc) Fetch(ctx context.Context, url string) (int, string, error) { return f(ctx, url) }

// HTTPFetcher is the real fetcher: a plain GET, redirects unfollowed, the
// body read up to a megabyte.
var HTTPFetcher = FetcherFunc(func(ctx context.Context, url string) (int, string, error) {
	resp, err := probeClient.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(body), err
})

// BrowserEnvPath is the file a static part's server answers with the values
// the forge gives its page (window.__FORGE_ENV__).
const BrowserEnvPath = "/_forge/env.js"

// BrowserExpectation is what the caller knows from trusted sources, not from
// the response being judged: whether the app's contract serves a static site,
// what the project's integrations give its page now, and what the running
// container was given when it started.
type BrowserExpectation struct {
	Static bool
	// Attached is what the page must be given: the integration settings as
	// they are now.
	Attached map[string]string
	// Installed is what the running container was started with, read from the
	// container itself; nil when that could not be established.
	Installed map[string]string
}

// ProbeVerdict is a probe's verdict and, when the verdict is unknown, whether
// the forge is answerable for it: Fault means the forge could not supply or
// establish what the check depends on, so no change to the app could make it
// pass. A change must never be sent back to its author over one.
type ProbeVerdict struct {
	Outcome Outcome
	Detail  string
	Fault   bool
}

// JudgeBrowserEnv judges a running app at base: a static site whose
// integrations give its page values must have been started with exactly
// those, must serve exactly what it was started with in /_forge/env.js, and
// its HTML must load that file by the supported convention. A server app is
// not judged. Conforming says the values are served and the markup loads
// them first; it does not say a browser ran the script (a content security
// policy can still stop it) or that reporting works.
//
// Why: a static app can adopt error reporting, pass review and ship, and
// never report, because nothing put the DSN in its page. The judgment is
// strict because looser ones were fooled: a mention of the path, a <base
// href>, a template and nomodule must not pass for a loader, and a forge
// delivery fault must be cannot-assess, never a pass or rework for the app.
func JudgeBrowserEnv(ctx context.Context, f Fetcher, base string, want BrowserExpectation) ProbeVerdict {
	unknown := func(detail string) ProbeVerdict {
		return ProbeVerdict{Outcome: CannotAssess, Detail: detail, Fault: true}
	}
	if !want.Static {
		return ProbeVerdict{Outcome: Conforms, Detail: "a server app reads its values from its environment; nothing to judge"}
	}
	if len(want.Attached) == 0 {
		return ProbeVerdict{Outcome: Conforms, Detail: "nothing attached gives the page a value"}
	}
	if want.Installed == nil {
		return unknown("the forge could not read what this deployment was started with, so it cannot say what its page is given")
	}
	if !sameValues(want.Installed, want.Attached) {
		return unknown(fmt.Sprintf("this deployment was started with %s, but the integrations now give the page %s: start it again so it serves today's settings", valueNames(want.Installed), valueNames(want.Attached)))
	}
	base = strings.TrimRight(base, "/")
	code, body, err := f.Fetch(ctx, base+BrowserEnvPath)
	switch {
	case err != nil:
		return unknown("the app did not answer, so the forge could not read what it serves the page: " + err.Error())
	case code != 200:
		return unknown(fmt.Sprintf("the forge's %s answered %d; the static server should always serve it", BrowserEnvPath, code))
	}
	served, ok := browserEnvValues(body)
	if !ok {
		return unknown("the forge's " + BrowserEnvPath + " could not be read")
	}
	if !sameValues(served, want.Installed) {
		return unknown(fmt.Sprintf("the forge's %s serves %s, not the %s this deployment was started with: the forge's delivery is at fault, not the app", BrowserEnvPath, valueNames(served), valueNames(want.Installed)))
	}
	named := valueNames(want.Attached)
	code, page, err := f.Fetch(ctx, base+"/")
	switch {
	case err != nil:
		return ProbeVerdict{Outcome: CannotAssess, Detail: "the page did not answer: " + err.Error()}
	case code != 200:
		return ProbeVerdict{Outcome: CannotAssess, Detail: fmt.Sprintf("the page answered %d", code)}
	}
	if why := loaderProblem(page); why != "" {
		return ProbeVerdict{Outcome: DoesNotConform, Detail: fmt.Sprintf("%s, so %s may never reach the app's code: put <script src=\"%s\"></script> in the page's <head> before any other script, with no attribute but src (a CSP nonce is fine), no <base href> in the page, and read window.__FORGE_ENV__", why, named, BrowserEnvPath)}
	}
	return ProbeVerdict{Outcome: Conforms, Detail: fmt.Sprintf("the forge serves %s at %s and the page's HTML loads it before its own scripts; whether a browser runs it is not checked", named, BrowserEnvPath)}
}

// javaScriptTypes are the script types a browser runs as a classic script
// (the HTML standard's JavaScript MIME type essence list).
var javaScriptTypes = map[string]bool{"": true, "application/ecmascript": true, "application/javascript": true, "application/x-ecmascript": true,
	"application/x-javascript": true, "text/ecmascript": true, "text/javascript": true, "text/javascript1.0": true, "text/javascript1.1": true,
	"text/javascript1.2": true, "text/javascript1.3": true, "text/javascript1.4": true, "text/javascript1.5": true, "text/jscript": true,
	"text/livescript": true, "text/x-ecmascript": true, "text/x-javascript": true}

// loaderProblem says how the page departs from the supported loader
// convention, or "" when it follows it: the first script a browser would run
// is a classic script in <head> whose src is exactly /_forge/env.js, with
// nothing that lets a browser skip, delay or re-address it. The convention is
// deliberately narrow; it is lint over markup, not evidence of execution.
func loaderProblem(page string) string {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return "the page could not be read as HTML"
	}
	var first, baseTag *html.Node
	var walk func(n *html.Node, inHead bool)
	var firstInHead bool
	walk = func(n *html.Node, inHead bool) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "template":
				return // inert until a script clones it
			case "head":
				inHead = true
			case "base":
				if _, ok := attrOf(n, "href"); ok && baseTag == nil {
					baseTag = n
				}
			case "script":
				if first == nil && executes(n) {
					first, firstInHead = n, inHead
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inHead)
		}
	}
	walk(doc, false)
	if baseTag != nil {
		return "the page sets <base href>, which can move where " + BrowserEnvPath + " loads from"
	}
	if first == nil {
		return "the page has no scripts and never loads " + BrowserEnvPath
	}
	if src, _ := attrOf(first, "src"); strings.TrimSpace(src) != BrowserEnvPath {
		return "the page's first script is not " + BrowserEnvPath
	}
	// Only attributes known not to change whether, when or what a browser
	// runs; anything else (async, defer, nomodule, integrity, type, a legacy
	// language) is refused, not reasoned about: a list of forbidden
	// attributes let language="vbscript" through.
	for _, a := range first.Attr {
		if k := strings.ToLower(a.Key); !loaderAttributes[k] {
			return "the page loads " + BrowserEnvPath + " with " + k + "; give it only src (and a CSP nonce)"
		}
	}
	if !firstInHead {
		return "the page loads " + BrowserEnvPath + " outside <head>"
	}
	return ""
}

// loaderAttributes are the only attributes the loader tag may carry.
var loaderAttributes = map[string]bool{"src": true, "nonce": true}

// executes says whether a browser would run this script element: a classic
// script or a module. A data block (JSON, an import map, a template language)
// never runs and may come first. As the HTML standard prepares a script, a
// missing type falls back to a legacy language attribute.
func executes(n *html.Node) bool {
	t, hasType := attrOf(n, "type")
	if lang, ok := attrOf(n, "language"); !hasType && ok && lang != "" {
		t = "text/" + lang
	}
	t = strings.ToLower(strings.TrimSpace(t))
	return javaScriptTypes[t] || t == "module"
}

// attrOf is an element's first attribute of that name, as a browser reads it.
func attrOf(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

// valueNames names a set of values for a sentence, never their contents.
func valueNames(values map[string]string) string {
	if len(values) == 0 {
		return "nothing"
	}
	return strings.Join(sortedKeys(values), ", ")
}

// sameValues compares keys as well as values: an absent key is not an empty
// one.
func sameValues(a, b map[string]string) bool { return maps.Equal(a, b) }

// browserEnvValues reads the values from the file the forge serves:
// window.__FORGE_ENV__ = Object.freeze(Object.assign(new Object(), {json}));
func browserEnvValues(body string) (map[string]string, bool) {
	const open = "Object.assign(new Object(), "
	i := strings.Index(body, open)
	j := strings.LastIndex(body, "));")
	if i < 0 || j < i+len(open) {
		return nil, false
	}
	values := map[string]string{}
	raw := strings.TrimSpace(body[i+len(open) : j])
	if raw == "" {
		return values, true
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, false
	}
	return values, true
}

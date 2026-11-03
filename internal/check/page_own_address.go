package check

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// JudgePageOwnAddress opens the front door's root. Each HTTP Location is
// resolved against the response URL; HTML assets use the final document URL
// and its first base href. Redirects must stay on the front door's host.
// Stylesheets and scripts on another host fail only if that host matches a
// caller-supplied app host pattern (exact or *.suffix); other hosts are third
// parties. No assets are fetched and JavaScript is not executed.
// A page that cannot be read, is refused or fails cannot establish this rule;
// JudgePage judges those failures separately.
func JudgePageOwnAddress(ctx context.Context, frontDoor string, hostPatterns []string) (Outcome, string) {
	current, err := url.Parse(frontDoor)
	if err != nil || current.Hostname() == "" {
		return CannotAssess, fmt.Sprintf("cannot read the front door address %q", frontDoor)
	}
	current.Path, current.RawPath, current.RawQuery, current.Fragment = "/", "", "", ""
	expected := current.Hostname()
	unknown := func(err error) (Outcome, string) {
		return CannotAssess, fmt.Sprintf("cannot judge addresses at %s: %v", current, err)
	}
	wrong := func(kind string, u *url.URL) (Outcome, string) {
		return DoesNotConform, fmt.Sprintf("%s %s uses another host; expected %s", kind, u, expected)
	}
	for redirects := 0; ; redirects++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return unknown(err)
		}
		resp, err := pageClient.Do(req)
		if err != nil {
			return unknown(err)
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			resp.Body.Close()
			location := resp.Header.Get("Location")
			if location == "" {
				return unknown(fmt.Errorf("redirect has no Location"))
			}
			ref, err := url.Parse(location)
			if err != nil {
				return unknown(err)
			}
			next := current.ResolveReference(ref)
			if !strings.EqualFold(next.Hostname(), expected) {
				return wrong("redirect", next)
			}
			if redirects == pageRedirects {
				return unknown(fmt.Errorf("too many redirects"))
			}
			current = next
			continue
		}
		if why := PageRefusal(resp.StatusCode, current.RequestURI()); why != "" {
			resp.Body.Close()
			return unknown(fmt.Errorf("%s", why))
		}
		// Bound the HTML read, but never call a truncated document conforming.
		body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		resp.Body.Close()
		if err != nil {
			return unknown(err)
		}
		if len(body) > 1<<20 {
			return unknown(fmt.Errorf("page exceeds the one megabyte inspection limit"))
		}
		doc, err := html.Parse(strings.NewReader(string(body)))
		if err != nil {
			return unknown(err)
		}
		base := current
		baseSeen := false
		var assets []string
		var walk func(*html.Node)
		walk = func(n *html.Node) {
			if n.Type == html.ElementNode {
				if n.Data == "base" && !baseSeen {
					if href, ok := attrOf(n, "href"); ok {
						baseSeen = true
						if ref, err := url.Parse(strings.TrimSpace(href)); err == nil {
							base = current.ResolveReference(ref)
						}
					}
				}
				if n.Data == "script" {
					if src, ok := attrOf(n, "src"); ok {
						assets = append(assets, src)
					}
				}
				if n.Data == "link" {
					relValue, _ := attrOf(n, "rel")
					for _, rel := range strings.Fields(relValue) {
						if strings.EqualFold(rel, "stylesheet") {
							if href, ok := attrOf(n, "href"); ok {
								assets = append(assets, href)
							}
							break
						}
					}
				}
			}
			// Template contents are inert until script instantiates them.
			if n.Type == html.ElementNode && n.Data == "template" {
				return
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
		walk(doc)
		for _, asset := range assets {
			ref, err := url.Parse(strings.TrimSpace(asset))
			if err != nil {
				return unknown(fmt.Errorf("cannot resolve asset %q: %w", asset, err))
			}
			resolved := base.ResolveReference(ref)
			host := strings.ToLower(resolved.Hostname())
			if strings.EqualFold(host, expected) {
				continue
			}
			for _, pattern := range hostPatterns {
				pattern = strings.ToLower(pattern)
				matches := host == pattern
				if strings.HasPrefix(pattern, "*.") {
					matches = strings.HasSuffix(host, pattern[1:])
				}
				if matches {
					return wrong("asset", resolved)
				}
			}
		}
		return Conforms, fmt.Sprintf("page %s keeps redirects and app assets on %s", current, expected)
	}
}

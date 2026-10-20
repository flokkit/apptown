package check

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func servedEnv(json string) string {
	return "window.__APPTOWN_ENV__ = Object.freeze(Object.assign(new Object(), " + json + "));"
}

const dsn = "https://k@errors.example/3"

// Judged from what AppTown knows, not what the response says: a static site
// given values must have been started with exactly those, serve exactly what
// it was started with, and load /_apptown/env.js by the supported convention. A
// server app, or a static site given nothing, is not judged. Anything the
// AppTown supplies and cannot establish is its own fault, never the app's.
func TestAStaticPageMustLoadTheValuesTheForgeGivesIt(t *testing.T) {
	t.Parallel()
	values := map[string]string{"SENTRY_DSN": dsn}
	given := BrowserExpectation{Static: true, Attached: values, Installed: values}
	served := servedEnv(`{"SENTRY_DSN":"` + dsn + `"}`)
	loads := `<html><head><script src="/_apptown/env.js"></script><script src="game.js"></script></head></html>`
	for _, tc := range []struct {
		name       string
		want       BrowserExpectation
		envCode    int
		envBody    string
		page       string
		outcome    Outcome
		fault      bool
		detailHave string
	}{
		{"a server app, whatever it serves", BrowserExpectation{Static: false}, 200, "<html>SPA fallback</html>", "", Conforms, false, "server app"},
		{"a static site given nothing", BrowserExpectation{Static: true}, 404, "", "", Conforms, false, "nothing attached"},
		// Nothing attached requires nothing, whatever an older start carried.
		{"a static site whose values were detached since it started", BrowserExpectation{Static: true, Installed: values}, 404, "", "", Conforms, false, "nothing attached"},
		{"loads it first", given, 200, served, loads, Conforms, false, "whether a browser runs it is not checked"},
		// Only src and a nonce may be on the loader, and a legacy language
		// decides whether a script runs at all.
		{"a legacy language loader", given, 200, served, `<head><script language="vbscript" src="/_apptown/env.js"></script><script src="game.js"></script></head>`, DoesNotConform, false, "first script is not"},
		{"after a script no browser runs", given, 200, served, `<head><script language="vbscript">x</script><script src="/_apptown/env.js"></script><script src="game.js"></script></head>`, Conforms, false, "loads it before"},
		{"with a nonce", given, 200, served, `<head><script nonce="r4nd0m" src="/_apptown/env.js"></script><script src="game.js"></script></head>`, Conforms, false, "loads it before"},
		{"with crossorigin", given, 200, served, `<head><script crossorigin src="/_apptown/env.js"></script></head>`, DoesNotConform, false, "only src"},
		{"started with other empty-valued keys", BrowserExpectation{Static: true, Attached: map[string]string{"EXPECTED": ""}, Installed: map[string]string{"OTHER": ""}}, 200, servedEnv(`{"OTHER":""}`), loads, CannotAssess, true, "start it again"},
		{"after a data block", given, 200, served, `<head><script type="application/json">{}</script><script src="/_apptown/env.js"></script><script src="game.js"></script></head>`, Conforms, false, "loads it before"},
		{"in markup a browser repairs", given, 200, served, `<head><script src="/_apptown/env.js"></script><div><script src="game.js">`, Conforms, false, "loads it before"},
		{"never loads it", given, 200, served, `<script src="game.js"></script>`, DoesNotConform, false, "first script is not"},
		// A mention is not a loader.
		{"only in a comment", given, 200, served, `<!-- <script src="/_apptown/env.js"></script> --><script src="game.js"></script>`, DoesNotConform, false, "first script is not"},
		{"after the app's code", given, 200, served, `<script src="game.js"></script><script src="/_apptown/env.js"></script>`, DoesNotConform, false, "first script is not"},
		{"after inline app code", given, 200, served, `<script>start()</script><script src="/_apptown/env.js"></script>`, DoesNotConform, false, "first script is not"},
		{"async", given, 200, served, `<script async src="/_apptown/env.js"></script><script src="game.js"></script>`, DoesNotConform, false, "with async"},
		{"deferred", given, 200, served, `<script defer src="/_apptown/env.js"></script>`, DoesNotConform, false, "with defer"},
		{"as a module", given, 200, served, `<script type="module" src="/_apptown/env.js"></script>`, DoesNotConform, false, "with type"},
		{"from another origin", given, 200, served, `<script src="https://other.example/_apptown/env.js"></script>`, DoesNotConform, false, "first script is not"},
		{"in the body", given, 200, served, `<html><head></head><body><script src="/_apptown/env.js"></script></body></html>`, DoesNotConform, false, "outside <head>"},
		{"with integrity", given, 200, served, `<script integrity="sha384-x" src="/_apptown/env.js"></script>`, DoesNotConform, false, "with integrity"},
		// The convention is narrow so that markup a browser would load elsewhere, or skip, never passes.
		{"relative", given, 200, served, `<script src="_apptown/env.js"></script>`, DoesNotConform, false, "first script is not"},
		{"a base moving a root-relative loader", given, 200, served, `<base href="https://cdn.example/"><script src="/_apptown/env.js"></script><script src="game.js"></script>`, DoesNotConform, false, "<base href>"},
		{"a base moving a relative loader", given, 200, served, `<base href="/assets/"><script src="_apptown/env.js"></script><script src="game.js"></script>`, DoesNotConform, false, "<base href>"},
		{"in an inert template", given, 200, served, `<template><script src="/_apptown/env.js"></script></template><script src="game.js"></script>`, DoesNotConform, false, "first script is not"},
		{"nomodule", given, 200, served, `<script nomodule src="/_apptown/env.js"></script><script src="game.js"></script>`, DoesNotConform, false, "with nomodule"},
		{"a repeated src, read as a browser reads it", given, 200, served, `<script src="game.js" src="/_apptown/env.js"></script>`, DoesNotConform, false, "first script is not"},
		// What AppTown supplies, it must establish; failing that is its own
		// fault, never a pass and never the app's to fix.
		{"started with values AppTown cannot read", BrowserExpectation{Static: true, Attached: values}, 200, served, loads, CannotAssess, true, "could not read what this deployment was started with"},
		{"started before the settings changed", BrowserExpectation{Static: true, Attached: values, Installed: map[string]string{}}, 200, servedEnv(""), loads, CannotAssess, true, "start it again"},
		{"served empty though started with values", given, 200, servedEnv(""), loads, CannotAssess, true, "delivery is at fault"},
		{"served the wrong values", given, 200, servedEnv(`{"SENTRY_DSN":"https://other@x/1"}`), loads, CannotAssess, true, "delivery is at fault"},
		{"not served by a static site", given, 404, "", loads, CannotAssess, true, "answered 404"},
		{"an unreadable file", given, 200, "oops", loads, CannotAssess, true, "could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := FetcherFunc(func(_ context.Context, url string) (int, string, error) {
				if strings.HasSuffix(url, BrowserEnvPath) {
					return tc.envCode, tc.envBody, nil
				}
				return 200, tc.page, nil
			})
			got := JudgeBrowserEnv(context.Background(), f, "http://127.0.0.1:1234/", tc.want)
			if got.Outcome != tc.outcome || got.Fault != tc.fault || !strings.Contains(got.Detail, tc.detailHave) {
				t.Fatalf("got %v fault=%v %q", got.Outcome, got.Fault, got.Detail)
			}
		})
	}
	down := FetcherFunc(func(context.Context, string) (int, string, error) { return 0, "", errors.New("refused") })
	if got := JudgeBrowserEnv(context.Background(), down, "http://127.0.0.1:1", given); got.Outcome != CannotAssess || !got.Fault {
		t.Fatalf("an app whose values cannot be read: %+v", got)
	}
	pageDown := FetcherFunc(func(_ context.Context, url string) (int, string, error) {
		if strings.HasSuffix(url, BrowserEnvPath) {
			return 200, served, nil
		}
		return 0, "", errors.New("refused")
	})
	if got := JudgeBrowserEnv(context.Background(), pageDown, "http://127.0.0.1:1", given); got.Outcome != CannotAssess || got.Fault {
		t.Fatalf("a page that does not answer is not AppTown's fault: %+v", got)
	}
	if ps, err := ParseProbe("browser-env"); err != nil || !ps.BrowserEnv {
		t.Fatalf("parse: %+v %v", ps, err)
	}
}

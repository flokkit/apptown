package appcontract

import (
	"regexp"
	"strings"
)

// A static site's files are cached by what their names promise, the same on
// every target that serves them (design/subsystems/deploy-targets.md,
// "Static site caching"). A file whose name carries its content's hash
// never changes under that name, so a browser keeps it for a year. Any
// other file, HTML included, is revalidated on every use: an unchanged one
// costs a 304, and a changed one is never served stale under a fresh page.
// AppTown's own two files are never cached at all.
const (
	CacheImmutable   = "public, max-age=31536000, immutable"
	CacheRevalidate  = "no-cache"
	cacheNever       = "no-store"
	browserValuesKey = "_apptown/env.js"
)

// HashedAssetPattern recognises a content-hashed file name, conservatively:
// <stem>.<hash>.<ext> or <stem>-<hash>.<ext>, where <ext> is a build
// output type and <hash> is either 8 characters of [A-Za-z0-9_-] with at
// least one digit (Vite and Rollup, Create React App) or 16 or more
// lowercase hex characters (webpack). A hash it misses only costs a revalidation; a
// hand-made name that matches (app-version2.js) and changes in place is the
// accepted failure mode. It is one RE2 expression with no braces, so the
// Docker host's file server (Caddy's path_regexp) and the bucket upload
// apply the same text.
var HashedAssetPattern = hashedAssetPattern()

var hashedAsset = regexp.MustCompile(HashedAssetPattern)

func hashedAssetPattern() string {
	const w, hex = `[A-Za-z0-9_-]`, `[0-9a-f]`
	// Eight characters with a digit at one of the eight places.
	eight := make([]string, 8)
	for i := range eight {
		eight[i] = strings.Repeat(w, i) + `[0-9]` + strings.Repeat(w, 7-i)
	}
	long := strings.Repeat(hex, 16) + hex + `*`
	return `[.-](` + strings.Join(eight, `|`) + `|` + long + `)\.(js|mjs|css|wasm|woff2?|ttf|otf|eot|svg|png|jpe?g|gif|webp|avif|ico|map)$`
}

// StaticCacheControl is the Cache-Control a static site's file at key (its
// path in the site, with or without a leading slash) is served with.
func StaticCacheControl(key string) string {
	key = strings.TrimPrefix(key, "/")
	switch {
	case key == browserValuesKey || "/"+key == ReleasePath:
		return cacheNever
	case hashedAsset.MatchString(key):
		return CacheImmutable
	}
	return CacheRevalidate
}

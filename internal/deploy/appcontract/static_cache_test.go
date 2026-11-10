package appcontract

import "testing"

// A content-hashed name is kept for a year, AppTown's own files never, and
// everything else, HTML and unhashed CSS and JS included, is revalidated:
// a release is never served with the previous release's styles or script.
func TestStaticFilesAreCachedByWhatTheirNamesPromise(t *testing.T) {
	for key, want := range map[string]string{
		"assets/index-BdK3x9aQ.js":            CacheImmutable, // Vite
		"assets/index-B-x3Kq_a.css":           CacheImmutable, // Vite, a hash with - and _
		"static/js/main.3f2a9c1b.js":          CacheImmutable, // Create React App
		"/static/media/logo.6ce24c58.svg":     CacheImmutable,
		"main.8b5c3d2e1f0a9b7c6d5e.js":        CacheImmutable, // webpack contenthash
		"fonts/inter-Ab12Cd34.woff2":          CacheImmutable,
		"index.html":                          CacheRevalidate,
		"about/index-3f2a9c1b.html":           CacheRevalidate, // HTML is never immutable
		"styles.css":                          CacheRevalidate,
		"app.js":                              CacheRevalidate,
		"assets/app-settings.js":              CacheRevalidate, // eight letters, no digit
		"photo-2024.png":                      CacheRevalidate, // too short
		"assets/index-BdKqxYaQ.js":            CacheRevalidate, // a hash with no digit is missed: revalidated
		"report-3f2a9c1b.pdf":                 CacheRevalidate, // not a build output type
		"main.8b5c3d2e1f0a9b7c6d5e.js.gz":     CacheRevalidate,
		"_apptown/env.js":                     cacheNever,
		"/_apptown/release":                   cacheNever,
		"jquery-3.7.1.min.js":                 CacheRevalidate,
		"assets/vendor-chunk.0123456789ab.js": CacheRevalidate, // 12 hex: neither form
	} {
		if got := StaticCacheControl(key); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
}

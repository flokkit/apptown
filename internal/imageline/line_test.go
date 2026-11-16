package imageline

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNewestUsesTagsAndManifestHead(t *testing.T) {
	t.Parallel()
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tags := []string{"2-alpine", "2.10.2-alpine", "2.11-alpine", "2.11.9-alpine", "2.12.0-beta.1-alpine", "2.11.9-alpine3.22", "3.0.0-alpine", "21-jre-noble", "21.0.13_9-jre-noble", "nonroot"}
	var mu sync.Mutex
	var heads []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.WriteHeader(200)
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			// Two pages prove remote.List follows Link instead of silently truncating.
			page := tags[:4]
			if r.URL.Query().Get("last") == "page1" {
				page = tags[4:]
			} else {
				w.Header().Set("Link", `</v2/library/caddy/tags/list?last=page1>; rel="next"`)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "library/caddy", "tags": page})
		case strings.Contains(r.URL.Path, "/manifests/"):
			if r.Method != "HEAD" {
				t.Errorf("manifest request used %s", r.Method)
				w.WriteHeader(500)
				return
			}
			mu.Lock()
			heads = append(heads, r.URL.Path)
			mu.Unlock()
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.Header().Set("Content-Length", "200")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		ref   string
		scope Scope
		want  string
	}{
		{"caddy:2-alpine", Line, "2.11.9-alpine"},
		{"caddy:2.10.2-alpine@" + digest, Minor, "2.11.9-alpine"},
		{"caddy:2.10.1-alpine", Patch, "2.10.2-alpine"},
		{"caddy:2-alpine", Major, "3.0.0-alpine"},
		{"eclipse-temurin:21-jre-noble", Line, "21-jre-noble"},
		{"caddy:nonroot", Major, "nonroot"},
	} {
		got, err := Newest(t.Context(), tc.ref, tc.scope, Access{Mirror: server.URL})
		if err != nil || got.Tag != "index.docker.io/library/"+strings.Split(tc.ref, ":")[0]+":"+tc.want || got.Digest != digest || got.Registry != strings.TrimPrefix(server.URL, "http://") {
			t.Fatalf("%s/%s: %+v %v", tc.ref, tc.scope, got, err)
		}
		mu.Lock()
		head := heads[len(heads)-1]
		mu.Unlock()
		if head != "/v2/library/"+strings.Split(tc.ref, ":")[0]+"/manifests/"+tc.want {
			t.Fatal(head)
		}
	}
	direct := strings.TrimPrefix(server.URL, "http://") + "/library/caddy:2-alpine"
	if got, err := Newest(t.Context(), direct, Line, Access{Mirror: "http://unreachable.invalid"}); err != nil || got.Registry != strings.TrimPrefix(server.URL, "http://") {
		t.Fatalf("non-Hub registry used a Hub mirror: %+v %v", got, err)
	}
	if _, err := Newest(t.Context(), "caddy:9-alpine", Line, Access{Mirror: server.URL}); err == nil {
		t.Fatal("missing line answered")
	}
}

func TestLineNormalizationAndScopes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scope Scope
		key   string
	}{{Line, "2.10.2-alpine"}, {Patch, "2.10-alpine"}, {Minor, "2-alpine"}, {Major, "-alpine"}} {
		s, err := Parse("docker.io/caddy:2.10.2-alpine@sha256:"+strings.Repeat("a", 64), tc.scope)
		if err != nil || s.Key() != "index.docker.io/library/caddy:"+tc.key+"@"+string(tc.scope) {
			t.Fatalf("%+v %v", s, err)
		}
	}
	if _, err := Parse("caddy:2", "oops"); err == nil {
		t.Fatal("unknown scope accepted")
	}
}

package oci

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestImageObservationReadsExactConfigWithoutPublishingOrLayers(t *testing.T) {
	var observing atomic.Bool
	var writes, layers atomic.Int32
	repo := newRegistry(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if observing.Load() {
				if r.Method != "GET" && r.Method != "HEAD" {
					writes.Add(1)
				}
				if strings.Contains(r.URL.Path, "/blobs/") && strings.Contains(r.URL.Path, digestOf([]byte("layer one"))) {
					layers.Add(1)
				}
			}
			next.ServeHTTP(w, r)
		})
	}) + "/team/node"
	image := newImage(t, "amd64", "layer one")
	published, err := Publish(t.Context(), image.files, Repository{Name: repo}, "before", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	observing.Store(true)
	got, err := Observe(t.Context(), Source{Reference: published.Reference()})
	if err != nil || got.Manifest != image.digest() || got.ConfigDigest != "sha256:"+strings.TrimPrefix(image.config, "blobs/sha256/") || got.Platform != "linux/amd64" || got.Repository != repo {
		t.Fatalf("observation %+v %v", got, err)
	}
	if writes.Load() != 0 || layers.Load() != 0 {
		t.Fatalf("observation performed writes=%d layer reads=%d", writes.Load(), layers.Load())
	}
	var cfg map[string]any
	if err := json.Unmarshal(got.Config, &cfg); err != nil || cfg["architecture"] != "amd64" || cfg["rootfs"] != nil {
		t.Fatalf("retained config %s %v", got.Config, err)
	}
	if _, err := Observe(t.Context(), Source{Reference: repo + ":before"}); err == nil {
		t.Fatal("tag qualified as immutable image")
	}
}

func TestImageObservationRefusesChangedBlobAndUnavailableRegistry(t *testing.T) {
	var corrupt atomic.Bool
	repo := newRegistry(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if corrupt.Load() && r.Method == "GET" && strings.Contains(r.URL.Path, "/blobs/") {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"architecture":"arm64"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}) + "/team/node"
	image := newImage(t, "amd64", "layer one")
	published, err := Publish(t.Context(), image.files, Repository{Name: repo}, "before", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	corrupt.Store(true)
	if _, err := Observe(t.Context(), Source{Reference: published.Reference()}); err == nil {
		t.Fatal("different config bytes accepted")
	}
	unavailable := newRegistry(t, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	}) + "/team/node@" + image.digest()
	if _, err := Observe(t.Context(), Source{Reference: unavailable}); !errors.Is(err, ErrUnauthorised) {
		t.Fatalf("registry refusal: %v", err)
	}
}

func TestImageObservationDoesNotDropUnknownConfiguration(t *testing.T) {
	image := newImage(t, "amd64", "layer")
	var cfg map[string]any
	if err := json.Unmarshal(image.files[image.config], &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["config"] = map[string]any{"User": "node", "Env": []string{"NODE_VERSION=22.14.0"}, "unrecognized": true}
	raw := mustJSON(t, cfg)
	image.config = "blobs/sha256/" + strings.TrimPrefix(digestOf(raw), "sha256:")
	image.files[image.config] = raw
	var manifest map[string]any
	if err := json.Unmarshal(image.manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["config"] = descriptor("application/vnd.oci.image.config.v1+json", raw)
	image.manifest = mustJSON(t, manifest)
	image.files["blobs/sha256/"+strings.TrimPrefix(image.digest(), "sha256:")] = image.manifest
	image.files["index.json"] = mustJSON(t, map[string]any{"schemaVersion": 2, "manifests": []any{descriptor("application/vnd.oci.image.manifest.v1+json", image.manifest)}})
	repo := newRegistry(t, nil) + "/team/node"
	pub, err := Publish(t.Context(), image.files, Repository{Name: repo}, "before", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Observe(t.Context(), Source{Reference: pub.Reference()})
	if err != nil || !bytes.Contains(got.Config, []byte(`"unrecognized":true`)) {
		t.Fatalf("unknown config dropped: %s %v", got.Config, err)
	}
}

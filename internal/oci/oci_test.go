package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// layoutFiles is an OCI layout held in memory.
type layoutFiles map[string][]byte

func (f layoutFiles) Open(_ context.Context, name string) (io.ReadCloser, error) {
	data, ok := f[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func descriptor(mediaType string, b []byte) map[string]any {
	return map[string]any{"mediaType": mediaType, "digest": digestOf(b), "size": len(b)}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// testImage is one image for arch in an OCI layout, as a build exports it,
// with the digest of its manifest and the layout path of each layer.
type testImage struct {
	files    layoutFiles
	manifest []byte
	config   string // its layout path
	layers   []string
}

func (i testImage) digest() string { return digestOf(i.manifest) }

const layerType = "application/vnd.oci.image.layer.v1.tar+gzip"

func newImage(t *testing.T, arch string, layers ...string) testImage {
	t.Helper()
	img := testImage{files: layoutFiles{}}
	var descs []map[string]any
	var diffs []string
	for _, l := range layers {
		b := []byte(l)
		path := "blobs/sha256/" + strings.TrimPrefix(digestOf(b), "sha256:")
		img.files[path] = b
		img.layers = append(img.layers, path)
		descs = append(descs, descriptor(layerType, b))
		diffs = append(diffs, digestOf(b))
	}
	config := mustJSON(t, map[string]any{"architecture": arch, "os": "linux", "rootfs": map[string]any{"type": "layers", "diff_ids": diffs}})
	img.config = "blobs/sha256/" + strings.TrimPrefix(digestOf(config), "sha256:")
	img.files[img.config] = config
	img.manifest = mustJSON(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": descriptor("application/vnd.oci.image.config.v1+json", config), "layers": descs})
	img.files["blobs/sha256/"+strings.TrimPrefix(img.digest(), "sha256:")] = img.manifest
	img.files["index.json"] = mustJSON(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": []any{descriptor("application/vnd.oci.image.manifest.v1+json", img.manifest)}})
	return img
}

// newRegistry is an in-memory registry, its handler wrapped by wrap if
// given, and its host.
func newRegistry(t *testing.T, wrap func(http.Handler) http.Handler) string {
	t.Helper()
	h := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	if wrap != nil {
		h = wrap(h)
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return strings.TrimPrefix(s.URL, "http://")
}

// tags are the repository's tags; none for a repository never written.
func tags(t *testing.T, repo string) []string {
	t.Helper()
	r, err := name.NewRepository(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := remote.List(r)
	if err != nil && !strings.Contains(err.Error(), "NAME_UNKNOWN") {
		t.Fatal(err)
	}
	return got
}

// A published image is the registry's answer for its digest: the manifest
// the layout named, its media type and size, and the tag resolves to it.
func TestAPublishedImageIsConfirmedByItsDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newRegistry(t, nil) + "/team/app"
	img := newImage(t, "amd64", "layer one", "layer two")
	got, err := Publish(ctx, img.files, Repository{Name: repo}, "forge-run7-a1-web", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	want := Image{Repository: repo, Digest: img.digest(), MediaType: "application/vnd.oci.image.manifest.v1+json", Size: int64(len(img.manifest))}
	if got != want {
		t.Fatalf("published %+v, want %+v", got, want)
	}
	if got.Reference() != repo+"@"+img.digest() {
		t.Fatalf("reference %q", got.Reference())
	}
	confirmed, err := Confirm(ctx, Repository{Name: repo}, img.digest())
	if err != nil || confirmed != want {
		t.Fatalf("confirmed %+v %v", confirmed, err)
	}
	tag, err := name.NewTag(repo + ":forge-run7-a1-web")
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := remote.Head(tag)
	if err != nil || tagged.Digest.String() != img.digest() {
		t.Fatalf("the tag names %v %v", tagged, err)
	}
	// Publishing again sends nothing new and answers the same.
	again, err := Publish(ctx, img.files, Repository{Name: repo}, "forge-run7-a2-web", "linux/amd64")
	if err != nil || again != want {
		t.Fatalf("published again %+v %v", again, err)
	}
}

// A blob that is short, long or has another digest fails the publication
// as malformed, and nothing is tagged: whatever a transfer would make of
// the blob, the manifest is never written.
func TestABlobThatIsShortLongOrMismatchedIsRefusedAndNothingIsTagged(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func([]byte) []byte{
		"short":      func(b []byte) []byte { return b[:len(b)-1] },
		"long":       func(b []byte) []byte { return append(append([]byte{}, b...), 'x') },
		"mismatched": func(b []byte) []byte { c := append([]byte{}, b...); c[0] ^= 0xff; return c },
	} {
		for _, which := range []string{"layer", "config"} {
			t.Run(name+" "+which, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				repo := newRegistry(t, nil) + "/team/app"
				img := newImage(t, "amd64", "first layer", "second layer, the one changed")
				path := img.layers[1]
				if which == "config" {
					path = img.config
				}
				img.files[path] = change(img.files[path])
				_, err := Publish(ctx, img.files, Repository{Name: repo}, "forge-run7-a1-web", "linux/amd64")
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("a %s %s published: %v", name, which, err)
				}
				if got := tags(t, repo); len(got) != 0 {
					t.Fatalf("a %s %s left tags %v", name, which, got)
				}
				if _, err := Confirm(ctx, Repository{Name: repo}, img.digest()); !errors.Is(err, ErrNotFound) {
					t.Fatalf("a %s %s left its manifest: %v", name, which, err)
				}
			})
		}
	}
}

// Only one OCI image manifest for the release's platform is published: an
// index, attestations, several manifests or another platform are refused
// by name, before anything is written.
func TestAnIndexAttestationsOrAnotherPlatformAreRefused(t *testing.T) {
	t.Parallel()
	index := func(t *testing.T, img testImage, entries ...map[string]any) {
		img.files["index.json"] = mustJSON(t, map[string]any{"schemaVersion": 2, "manifests": entries})
	}
	for name, c := range map[string]struct {
		arch   string
		change func(*testing.T, testImage)
		says   string
	}{
		"two manifests": {"amd64", func(t *testing.T, img testImage) {
			d := descriptor("application/vnd.oci.image.manifest.v1+json", img.manifest)
			index(t, img, d, d)
		}, "2 manifests"},
		"an index": {"amd64", func(t *testing.T, img testImage) {
			index(t, img, descriptor("application/vnd.oci.image.index.v1+json", img.manifest))
		}, "index of images"},
		"an attestation manifest": {"amd64", func(t *testing.T, img testImage) {
			d := descriptor("application/vnd.oci.image.manifest.v1+json", img.manifest)
			d["annotations"] = map[string]string{"vnd.docker.reference.type": "attestation-manifest"}
			index(t, img, d)
		}, "attestation"},
		"a Docker manifest": {"amd64", func(t *testing.T, img testImage) {
			index(t, img, descriptor("application/vnd.docker.distribution.manifest.v2+json", img.manifest))
		}, "not an OCI image manifest"},
		"another platform":    {"arm64", func(*testing.T, testImage) {}, "linux/arm64, not linux/amd64"},
		"an unknown platform": {"riscv64", func(*testing.T, testImage) {}, "riscv64"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newRegistry(t, nil) + "/team/app"
			img := newImage(t, c.arch, "a layer")
			c.change(t, img)
			_, err := Publish(context.Background(), img.files, Repository{Name: repo}, "forge-run7-a1-web", "linux/amd64")
			if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("%s: %v", name, err)
			}
			if got := tags(t, repo); len(got) != 0 {
				t.Fatalf("%s left tags %v", name, got)
			}
		})
	}
}

// answering rewrites the registry's answer to a HEAD of a manifest by
// digest: its read-back.
func answering(header, value string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodHead || !strings.Contains(r.URL.Path, "/manifests/sha256:") {
				next.ServeHTTP(w, r)
				return
			}
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, r)
			maps.Copy(w.Header(), rec.Header())
			if rec.Code == http.StatusOK {
				w.Header().Set(header, value)
			}
			w.WriteHeader(rec.Code)
		})
	}
}

// A read-back that answers another digest, media type or size means the
// image is not published, though the registry took the write.
func TestAReadBackNamingSomethingElseIsNoPublication(t *testing.T) {
	t.Parallel()
	other := digestOf([]byte("another image"))
	for name, wrap := range map[string]func(http.Handler) http.Handler{
		"another digest":     answering("Docker-Content-Digest", other),
		"another media type": answering("Content-Type", "application/vnd.docker.distribution.manifest.v2+json"),
		"another size":       answering("Content-Length", "3"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newRegistry(t, wrap) + "/team/app"
			img := newImage(t, "amd64", "a layer")
			got, err := Publish(context.Background(), img.files, Repository{Name: repo}, "forge-run7-a1-web", "linux/amd64")
			if !errors.Is(err, ErrMalformed) || got != (Image{}) {
				t.Fatalf("a read-back with %s published %+v: %v", name, got, err)
			}
		})
	}
}

// requiring answers 401 to every request without the given login.
func requiring(user, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, p, ok := r.BasicAuth(); !ok || u != user || p != password {
				w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// A refused credential and an unreachable registry are different failures,
// and a missing image is a third.
func TestUnauthorisedUnavailableAndNotFoundAreDistinct(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	guarded := newRegistry(t, requiring("apptown", "right")) + "/team/app"
	img := newImage(t, "amd64", "a layer")
	_, err := Publish(ctx, img.files, Repository{Name: guarded, Credential: Credential{Username: "apptown", Password: "wrong"}}, "t", "linux/amd64")
	if !errors.Is(err, ErrUnauthorised) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused credential: %v", err)
	}
	// Who refused is read from the answer.
	if host, ok := RefusedBy(err); !ok || host != strings.TrimSuffix(guarded, "/team/app") {
		t.Fatalf("refused by %q %v", host, ok)
	}
	right := Repository{Name: guarded, Credential: Credential{Username: "apptown", Password: "right"}}
	if _, err := Publish(ctx, img.files, right, "t", "linux/amd64"); err != nil {
		t.Fatalf("the right credential: %v", err)
	}
	if _, err := Confirm(ctx, Repository{Name: guarded}, img.digest()); !errors.Is(err, ErrUnauthorised) {
		t.Fatalf("an anonymous read-back: %v", err)
	}
	if _, err := Confirm(ctx, right, digestOf([]byte("never written"))); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorised) {
		t.Fatalf("a missing image: %v", err)
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	closed := strings.TrimPrefix(gone.URL, "http://") + "/team/app"
	_, err = Publish(ctx, img.files, Repository{Name: closed}, "t", "linux/amd64")
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrUnauthorised) || errors.Is(err, ErrNotFound) {
		t.Fatalf("an unreachable registry: %v", err)
	}
	if _, ok := RefusedBy(err); ok {
		t.Fatalf("an unreachable registry read as a refusal: %v", err)
	}
	if _, err := Confirm(ctx, Repository{Name: closed}, img.digest()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unreachable read-back: %v", err)
	}
}

// A layout that cannot be read fails the publication with its own error,
// never as the registry being unavailable.
func TestAnUnreadableLayoutIsNotAnUnavailableRegistry(t *testing.T) {
	t.Parallel()
	repo := newRegistry(t, nil) + "/team/app"
	img := newImage(t, "amd64", "a layer")
	broken := brokenLayout{layoutFiles: img.files, path: img.layers[0]}
	_, err := Publish(context.Background(), broken, Repository{Name: repo}, "t", "linux/amd64")
	if err == nil || !errors.Is(err, errWorkerGone) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unreadable layout: %v", err)
	}
	if got := tags(t, repo); len(got) != 0 {
		t.Fatalf("an unreadable layout left tags %v", got)
	}
}

var errWorkerGone = errors.New("the worker went away")

type brokenLayout struct {
	layoutFiles
	path string
}

func (b brokenLayout) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if name == b.path {
		return nil, &url.Error{Op: "Get", URL: "http://worker:7070/" + name, Err: errWorkerGone}
	}
	return b.layoutFiles.Open(ctx, name)
}

// putIndex writes an image index naming each image, in order, for its
// config's architecture.
func putIndex(t *testing.T, repo, tag string, images ...testImage) {
	t.Helper()
	var entries []any
	for _, img := range images {
		var cfg struct{ Architecture string }
		if err := json.Unmarshal(img.files[img.config], &cfg); err != nil {
			t.Fatal(err)
		}
		d := descriptor("application/vnd.oci.image.manifest.v1+json", img.manifest)
		d["platform"] = map[string]string{"os": "linux", "architecture": cfg.Architecture}
		entries = append(entries, d)
	}
	body := mustJSON(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": entries})
	host, path, _ := strings.Cut(repo, "/")
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("http://%s/v2/%s/manifests/%s", host, path, tag), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("putting the index: %s", resp.Status)
	}
}

// A copy keeps the image's digest, from a digest or from an index resolved
// for the release's platform, and is confirmed where it was copied to.
// Another platform is refused.
func TestACopyKeepsItsDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	src := newRegistry(t, nil) + "/upstream/db"
	dst := Repository{Name: newRegistry(t, nil) + "/team/app"}
	amd, arm := newImage(t, "amd64", "amd64 layer"), newImage(t, "arm64", "arm64 layer")
	if _, err := Publish(ctx, amd.files, Repository{Name: src}, "amd", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(ctx, arm.files, Repository{Name: src}, "arm", "linux/arm64"); err != nil {
		t.Fatal(err)
	}
	putIndex(t, src, "16", amd, arm)

	byDigest, err := Copy(ctx, Source{Reference: src + "@" + amd.digest()}, dst, "forge-run7-a1-db", "linux/amd64")
	if err != nil || byDigest.Digest != amd.digest() || byDigest.Repository != dst.Name {
		t.Fatalf("copied by digest %+v %v", byDigest, err)
	}
	fromIndex, err := Copy(ctx, Source{Reference: src + ":16"}, dst, "forge-run7-a1-db-arm", "linux/arm64")
	if err != nil || fromIndex.Digest != arm.digest() {
		t.Fatalf("copied from the index %+v %v, want %s", fromIndex, err, arm.digest())
	}
	for _, d := range []string{amd.digest(), arm.digest()} {
		if _, err := Confirm(ctx, dst, d); err != nil {
			t.Fatalf("%s not in the destination: %v", d, err)
		}
	}
	if _, err := Copy(ctx, Source{Reference: src + ":arm"}, dst, "x", "linux/amd64"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an image for another platform: %v", err)
	}
	putIndex(t, src, "arm-only", arm)
	if _, err := Copy(ctx, Source{Reference: src + ":arm-only"}, dst, "x", "linux/amd64"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an index with no image for the platform: %v", err)
	}
	if _, err := Copy(ctx, Source{Reference: src + ":never"}, dst, "x", "linux/amd64"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing source: %v", err)
	}
}

// A tag points at an image the registry holds, by its digest, and moves
// when it is given to another; an image the registry does not have is not
// found, and nothing is tagged.
//
// Kills: a tag written as a new image (another digest); a missing image
// taken for tagged.
func TestATagNamesTheImageByItsDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := Repository{Name: newRegistry(t, nil) + "/team/app"}
	one, two := newImage(t, "amd64", "one"), newImage(t, "amd64", "two")
	for i, img := range []testImage{one, two} {
		if _, err := Publish(ctx, img.files, repo, fmt.Sprintf("built-%d", i), "linux/amd64"); err != nil {
			t.Fatal(err)
		}
	}
	answer := func(tag string) string {
		t.Helper()
		ref, err := name.NewTag(repo.Name + ":" + tag)
		if err != nil {
			t.Fatal(err)
		}
		d, err := remote.Head(ref)
		if err != nil {
			return ""
		}
		return d.Digest.String()
	}
	if err := Tag(ctx, repo, one.digest(), "release-3-part-web"); err != nil || answer("release-3-part-web") != one.digest() {
		t.Fatalf("tagged %q: %v", answer("release-3-part-web"), err)
	}
	if err := Tag(ctx, repo, two.digest(), "release-3-part-web"); err != nil || answer("release-3-part-web") != two.digest() {
		t.Fatalf("the tag moved to %q: %v", answer("release-3-part-web"), err)
	}
	missing := "sha256:" + strings.Repeat("0", 64)
	if err := Tag(ctx, repo, missing, "release-4-part-web"); !errors.Is(err, ErrNotFound) || answer("release-4-part-web") != "" {
		t.Fatalf("tagging an image the registry does not have: %v", err)
	}
}

// A machine's raw facts name a canonical platform; anything unknown is
// refused, never guessed.
func TestPlatformsAreNormalisedAndUnknownIsRefused(t *testing.T) {
	t.Parallel()
	for in, want := range map[[2]string]string{
		{"linux", "x86_64"}: "linux/amd64", {"linux", "amd64"}: "linux/amd64",
		{"linux", "aarch64"}: "linux/arm64", {"linux", "arm64"}: "linux/arm64",
	} {
		if got, err := Platform(in[0], in[1]); err != nil || got != want {
			t.Errorf("%v: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range [][2]string{{"linux", ""}, {"linux", "unknown"}, {"linux", "riscv64"}, {"linux", "i386"}, {"windows", "x86_64"}, {"", "x86_64"}} {
		if got, err := Platform(in[0], in[1]); !errors.Is(err, ErrUnsupported) || got != "" {
			t.Errorf("%v: %q %v, want refused", in, got, err)
		}
	}
}

// A source is named as a project names an image, with Docker's defaults
// (docker.io, its library namespace); it reaches the registry rather than
// being refused as malformed. Invalid syntax is still refused.
func TestACopySourceTakesDockersOrdinaryNames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stop at the first registry request: only parsing is under test
	to := Repository{Name: "registry.example.com/team/app"}
	for _, ref := range []string{"redis:7", "docker.io/redis:7", "example/cache:7", "index.docker.io/library/redis:7"} {
		if _, err := Copy(ctx, Source{Reference: ref}, to, "release", "linux/amd64"); errors.Is(err, ErrMalformed) {
			t.Errorf("%s refused before the registry was asked: %v", ref, err)
		}
	}
	if _, err := Copy(ctx, Source{Reference: "Not A Reference!"}, to, "release", "linux/amd64"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("invalid syntax: %v", err)
	}
}

// A deleted image is gone from its repository; deleting it again, or an
// image never there, is not an error.
func TestADeletedImageIsGone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newRegistry(t, nil) + "/evals/shop-copy"
	img := newImage(t, "amd64", "layer one")
	if _, err := Publish(ctx, img.files, Repository{Name: repo}, "forge-run1-a1-web", "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, Repository{Name: repo}, img.digest()); err != nil {
		t.Fatal(err)
	}
	if _, err := Confirm(ctx, Repository{Name: repo}, img.digest()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still there: %v", err)
	}
	if err := Delete(ctx, Repository{Name: repo}, img.digest()); err != nil {
		t.Fatalf("deleting again: %v", err)
	}
}

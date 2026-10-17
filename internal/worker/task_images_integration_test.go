//go:build integration

package worker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flokkit.io/forge/internal/execution"
	"flokkit.io/forge/internal/oci"
	"flokkit.io/forge/internal/worker"
	"flokkit.io/forge/internal/worker/workertest"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// Image results on a worker, with the worker server in this process and this machine's
// Docker.

// A task's images are captured when it has exited, each in its own state,
// and the task's exit is kept whatever they hold: a link inside a layout,
// a layout reached through a link, and one over its admitted size are
// refused or too large, with nothing of them served, and one not left is
// absent. A file of a present image is served whole. Once the task is
// released a new open is gone, and so is its listing; a read already open
// finishes with every byte.
func TestIntegrationImageCapturesAreRefusedWithTheTaskExitKept(t *testing.T) {
	ca := workertest.NewCA(t)
	state := t.TempDir()
	namespace := fmt.Sprintf("images%d", time.Now().UnixNano()%1_000_000_000)
	removeNamespace(t, namespace)
	driver := workertest.Driver(t, ca, workertest.StartServer(t, ca, &worker.Server{State: state, Tasks: execution.DockerTasks{Namespace: namespace}}))
	ctx := context.Background()
	hub, base := imageHub(t)
	blob := strings.Repeat("a", 64)
	script := `set -e
mkdir -p /forge/state/out/good/blobs/sha256 /forge/state/out/inner/blobs/sha256 /forge/state/out/big
echo '{"schemaVersion":2,"manifests":[]}' > /forge/state/out/good/index.json
head -c 8388608 /dev/urandom > /forge/state/out/good/blobs/sha256/` + blob + `
cp /forge/state/out/good/index.json /forge/state/out/inner/index.json
ln -s /etc/passwd /forge/state/out/inner/blobs/sha256/` + blob + `
ln -s /etc /forge/state/out/linked
head -c 2048 /dev/zero > /forge/state/out/big/index.json
exit 3
`
	spec := execution.TaskSpec{Key: namespace + "-build", Image: "alpine:3", Argv: []string{"sh", "-c", script},
		Admission: execution.Admission{Profile: "worker", Network: execution.NetworkNone, Expires: time.Now().Add(time.Hour)},
		Source:    &execution.TaskSource{Repository: hub, Commit: base},
		Images: []execution.ImageResult{
			{Name: "good", Path: "/forge/state/out/good", Max: 16 << 20},
			{Name: "inner", Path: "/forge/state/out/inner", Max: 16 << 20},
			{Name: "linked", Path: "/forge/state/out/linked", Max: 16 << 20},
			{Name: "big", Path: "/forge/state/out/big", Max: 1024},
			{Name: "absent", Path: "/forge/state/out/absent", Max: 1024},
		}}
	created, err := driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if _, err := driver.ImageResults(ctx, ref); err == nil || !strings.Contains(err.Error(), "not exited") {
		t.Fatalf("the images of a task that has not run: %v", err)
	}
	if err := driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if code, err := driver.Wait(ctx, ref); err != nil || code != 3 {
		page, _ := driver.Output(ctx, ref, execution.OutputRead{})
		t.Fatalf("the task: %d %v\n%s", code, err, page.Data)
	}
	got, err := driver.ImageResults(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]execution.ImageCapture{
		"good":   {State: execution.ResultPresent, Size: 8388608 + int64(len(`{"schemaVersion":2,"manifests":[]}`)+1)},
		"inner":  {State: execution.ResultRefused, Reason: execution.RefusedSymlink},
		"linked": {State: execution.ResultRefused, Reason: execution.RefusedSymlink},
		"big":    {State: execution.ResultTooLarge, Size: 2048},
		"absent": {State: execution.ResultAbsent},
	}
	if len(got) != len(want) {
		t.Errorf("%d images, want %d: %+v", len(got), len(want), got)
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("the image %s: %+v, want %+v", name, got[name], w)
		}
	}
	if obs, err := driver.Observe(ctx, ref); err != nil || obs.State != execution.TaskExited || obs.ExitCode != 3 {
		t.Errorf("the task after its images were read: %+v %v", obs, err)
	}
	for _, name := range []string{"inner", "linked", "big", "absent"} {
		if body, _, err := driver.ImageFile(ctx, ref, name, "index.json"); err == nil {
			body.Close()
			t.Errorf("the %s image's index was served", name)
		}
	}

	// The blob as the task left it, before the release removes it.
	left, err := os.ReadFile(filepath.Join(state, "workspace-state", spec.Key, "out", "good", "blobs", "sha256", blob))
	if err != nil {
		t.Fatal(err)
	}
	open, size, err := driver.ImageFile(ctx, ref, "good", "blobs/sha256/"+blob)
	if err != nil || size != int64(len(left)) {
		t.Fatalf("opening the blob: %d %v", size, err)
	}
	defer open.Close()
	first := make([]byte, 1)
	if _, err := io.ReadFull(open, first); err != nil {
		t.Fatal(err)
	}
	if err := driver.Release(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if body, _, err := driver.ImageFile(ctx, ref, "good", "blobs/sha256/"+blob); !errors.Is(err, execution.ErrImageGone) {
		if body != nil {
			body.Close()
		}
		t.Errorf("a new open after the release: %v", err)
	}
	if _, err := driver.ImageResults(ctx, ref); !errors.Is(err, execution.ErrImageGone) {
		t.Errorf("the images after the release: %v", err)
	}
	rest, err := io.ReadAll(open)
	if err != nil {
		t.Fatalf("the read open before the release: %v", err)
	}
	if read := append(first, rest...); sha256.Sum256(read) != sha256.Sum256(left) {
		t.Errorf("the read open before the release ended with %d bytes, not the %d the task left", len(read), len(left))
	}
	if _, err := os.Lstat(filepath.Join(state, "task-results", spec.Key)); !os.IsNotExist(err) {
		t.Errorf("the captured images outlived the release: %v", err)
	}
}

// ociIndex and ociManifest are what this test reads of an OCI layout.
type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}
type ociIndex struct {
	Manifests []ociDescriptor `json:"manifests"`
}
type ociManifest struct {
	MediaType string          `json:"mediaType"`
	Config    ociDescriptor   `json:"config"`
	Layers    []ociDescriptor `json:"layers"`
}

// A release build in the task's private Docker, with the production
// flags (one platform, no provenance, no SBOM), exports an OCI layout whose
// index names exactly one image manifest for that platform. The worker
// captures it after the build and its daemon stopped, keeps it across a
// worker restart, and serves its index and every blob the manifest names,
// each with its descriptor's length and digest.
//
// Composed with step 1: the forge publishes that export, read file by file
// from the worker, into a registry, and what the registry reads back is the
// manifest's digest; a read-back naming another is no publication. A blob
// cut short on the wire, or read after the task was released (gone), fails
// the publication with the layout's own error, and nothing is tagged: never
// a short blob published.
func TestIntegrationAReleaseBuildExportsOneImageManifest(t *testing.T) {
	f := newPrivateFixture(t)
	ctx := context.Background()
	platform, err := oci.Platform("linux", f.profile.Architecture)
	if err != nil {
		t.Fatal(err)
	}
	script := `set -e
docker buildx build --platform ` + platform + ` --provenance=false --sbom=false --output type=oci,dest=/forge/state/out/greeting,tar=false -f /work/Dockerfile /work
`
	spec := f.spec("release", script, &execution.PrivateDocker{Daemon: true})
	spec.Images = []execution.ImageResult{{Name: "greeting", Path: "/forge/state/out/greeting", Max: 512 << 20}}
	created, err := f.driver.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.TaskRef{Key: spec.Key, ProviderID: created.ID}
	if err := f.driver.Start(ctx, ref); err != nil {
		t.Fatal(err)
	}
	code, err := f.driver.Wait(ctx, ref)
	if err != nil || code != 0 {
		page, _ := f.driver.Output(ctx, ref, execution.OutputRead{})
		t.Fatalf("the build: %d %v\n%s", code, err, page.Data)
	}
	_, daemon := f.names(spec.Key)
	if running := inspect(t, "{{.State.Running}}", daemon); running != "false" {
		t.Errorf("the build's daemon after it exited: running %s", running)
	}
	f.restart()
	images, err := f.driver.ImageResults(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if c := images["greeting"]; c.State != execution.ResultPresent || c.Size <= 0 {
		t.Fatalf("the captured image: %+v", c)
	}

	read := func(file string) []byte {
		t.Helper()
		body, size, err := f.driver.ImageFile(ctx, ref, "greeting", file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		defer body.Close()
		got, err := io.ReadAll(body)
		if err != nil || int64(len(got)) != size {
			t.Fatalf("%s: %d of %d bytes, %v", file, len(got), size, err)
		}
		return got
	}
	blob := func(d ociDescriptor) []byte {
		t.Helper()
		hexSum, ok := strings.CutPrefix(d.Digest, "sha256:")
		if !ok {
			t.Fatalf("a descriptor names %q", d.Digest)
		}
		got := read("blobs/sha256/" + hexSum)
		sum := sha256.Sum256(got)
		if hex.EncodeToString(sum[:]) != hexSum || int64(len(got)) != d.Size {
			t.Fatalf("the blob %s: %d bytes with digest %x, want %d", d.Digest, len(got), sum, d.Size)
		}
		return got
	}
	var index ociIndex
	if err := json.Unmarshal(read("index.json"), &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != 1 || index.Manifests[0].MediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Fatalf("the index names %+v, not exactly one image manifest", index.Manifests)
	}
	var manifest ociManifest
	if err := json.Unmarshal(blob(index.Manifests[0]), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Config.MediaType != "application/vnd.oci.image.config.v1+json" || len(manifest.Layers) == 0 {
		t.Fatalf("the manifest: %+v", manifest)
	}
	var config struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	}
	if err := json.Unmarshal(blob(manifest.Config), &config); err != nil {
		t.Fatal(err)
	}
	if config.OS+"/"+config.Architecture != platform {
		t.Errorf("the image's platform is %s/%s, not the build's %s", config.OS, config.Architecture, platform)
	}
	for _, layer := range manifest.Layers {
		blob(layer)
	}

	// Published through step 1, read from the worker file by file.
	tag := "forge-run1-a1-greeting"
	repo := oci.Repository{Name: inMemoryRegistry(t) + "/forge/greeting"}
	published, err := oci.Publish(ctx, execution.ImageLayout{Files: f.driver, Ref: ref, Image: "greeting"}, repo, tag, platform)
	if err != nil {
		t.Fatal(err)
	}
	if published.Digest != index.Manifests[0].Digest || published.Size != index.Manifests[0].Size {
		t.Errorf("published %s (%d bytes), the export's manifest is %s (%d bytes)", published.Digest, published.Size, index.Manifests[0].Digest, index.Manifests[0].Size)
	}
	if byTag := tagDigest(t, repo.Name+":"+tag); byTag != index.Manifests[0].Digest {
		t.Errorf("the tag resolves to %s, not the export's manifest %s", byTag, index.Manifests[0].Digest)
	}
	// A registry that takes the write but reads back another digest has
	// not published the export: the read-back is the record.
	lying := oci.Repository{Name: inMemoryRegistryWith(t, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodHead || !strings.Contains(r.URL.Path, "/manifests/sha256:") {
				h.ServeHTTP(w, r)
				return
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			maps.Copy(w.Header(), rec.Header())
			w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("0", 64))
			w.WriteHeader(rec.Code)
		})
	}) + "/forge/greeting"}
	if got, err := oci.Publish(ctx, execution.ImageLayout{Files: f.driver, Ref: ref, Image: "greeting"}, lying, tag, platform); err == nil {
		t.Errorf("a read-back naming another digest published %+v", got)
	}

	// A layer cut short on the wire, by a worker over the same state.
	layer := "blobs/sha256/" + strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:")
	cutting := workertest.StartServerWith(t, f.ca, &worker.Server{State: f.state, Tasks: execution.DockerTasks{Namespace: f.namespace}},
		func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, layer) {
					w = &cutShort{ResponseWriter: w, left: manifest.Layers[0].Size / 2}
				}
				h.ServeHTTP(w, r)
			})
		})
	short := oci.Repository{Name: inMemoryRegistry(t) + "/forge/greeting"}
	// It fails as the transport's error, the layout's own, never as the
	// registry's refusal of a short blob.
	if _, err := oci.Publish(ctx, execution.ImageLayout{Files: workertest.Driver(t, f.ca, cutting), Ref: ref, Image: "greeting"}, short, tag, platform); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a layer cut short on the wire: %v", err)
	}
	if got := tagDigest(t, short.Name+":"+tag); got != "" {
		t.Errorf("a layer cut short left the tag at %s", got)
	}

	// The task is released once the publisher has read the manifest and
	// config, before it reads a layer: the layer is gone.
	gone := oci.Repository{Name: inMemoryRegistry(t) + "/forge/greeting"}
	released := false
	release := func(name string) {
		if name == layer && !released {
			released = true
			if err := f.driver.Release(ctx, ref); err != nil {
				t.Error(err)
			}
		}
	}
	_, err = oci.Publish(ctx, execution.ImageLayout{Files: beforeOpen{f.driver, release}, Ref: ref, Image: "greeting"}, gone, tag, platform)
	if !released || !errors.Is(err, execution.ErrImageGone) {
		t.Errorf("a layer read after the release (released %v): %v", released, err)
	}
	if got := tagDigest(t, gone.Name+":"+tag); got != "" {
		t.Errorf("a layer read after the release left the tag at %s", got)
	}
}

// beforeOpen runs before each image file is opened: a release at
// a chosen moment.
type beforeOpen struct {
	execution.ImageFiles
	before func(name string)
}

func (b beforeOpen) ImageFile(ctx context.Context, ref execution.TaskRef, image, file string) (io.ReadCloser, int64, error) {
	b.before(file)
	return b.ImageFiles.ImageFile(ctx, ref, image, file)
}

// cutShort passes on at most left bytes of a reply, then drops the
// connection, as a reply cut short on the wire.
type cutShort struct {
	http.ResponseWriter
	left int64
}

func (c *cutShort) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		_, _ = c.ResponseWriter.Write(p[:c.left])
		c.ResponseWriter.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	c.left -= int64(len(p))
	return c.ResponseWriter.Write(p)
}

// inMemoryRegistry is go-containerregistry's registry, its host.
func inMemoryRegistry(t *testing.T) string {
	t.Helper()
	return inMemoryRegistryWith(t, func(h http.Handler) http.Handler { return h })
}

// inMemoryRegistryWith is an in-memory registry with its handler wrapped.
func inMemoryRegistryWith(t *testing.T, wrap func(http.Handler) http.Handler) string {
	t.Helper()
	s := httptest.NewServer(wrap(registry.New(registry.Logger(log.New(io.Discard, "", 0)))))
	t.Cleanup(s.Close)
	return strings.TrimPrefix(s.URL, "http://")
}

// tagDigest is the digest a tag resolves to, or "" for none.
func tagDigest(t *testing.T, ref string) string {
	t.Helper()
	tagRef, err := name.NewTag(ref)
	if err != nil {
		t.Fatal(err)
	}
	d, err := remote.Head(tagRef)
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return ""
		}
		t.Fatal(err)
	}
	return d.Digest.String()
}

package s3bucket

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/s3bucket/s3fake"
	"flokkit.io/forge/internal/deploy/staticsite"
	"flokkit.io/forge/internal/ids/idstest"
)

const (
	accessKey = "AKTESTACCESS"
	secretKey = "test-secret-key-never-shown"
)

// world is one store, one registry and one deployment's records.
type world struct {
	t        *testing.T
	store    *s3fake.Store
	registry string
	browser  map[string]string
	owned    []string
	suffix   string
	logs     []string
	handle   map[string]any // the deployment's handle, as settlement keeps it
	noted    map[string]any // the latest attempt's checkpoint
	gen      int64
}

func newWorld(t *testing.T) *world {
	h := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	reg := httptest.NewServer(h)
	t.Cleanup(reg.Close)
	return &world{t: t, store: s3fake.New(t, accessKey), registry: strings.TrimPrefix(reg.URL, "http://"), browser: map[string]string{}, handle: map[string]any{}, suffix: "aaaa0001"}
}

func (w *world) driver() *Driver {
	cfg := Config{Endpoint: w.store.API.URL, Region: "syd1", WebsiteTemplate: w.store.WebsiteTemplate(), AccessKeyFrom: "A", SecretKeyFrom: "S"}
	return NewDriver(Dependencies{
		Client: func(context.Context) (*Client, Config, error) {
			return &Client{Endpoint: cfg.Endpoint, Region: cfg.Region, AccessKey: accessKey, SecretKey: secretKey}, cfg, nil
		},
		Slug: "Game Site",
		Own: func(bucket string) error {
			w.owned = append(w.owned, bucket)
			w.handle["bucket"] = bucket
			return nil
		},
		BrowserValues: func(context.Context) (map[string]string, error) { return w.browser, nil },
	}, Options{WitnessTries: 2, WitnessInterval: time.Millisecond, Suffix: func() string { return w.suffix }})
}

// image pushes a self-serving static image whose /srv holds files.
func (w *world) image(files map[string]string) string {
	t := w.t
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "srv/", Typeflag: tar.TypeDir, Mode: 0o755})
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		tw.WriteHeader(&tar.Header{Name: "srv/" + k, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[k]))})
		tw.Write([]byte(files[k]))
	}
	tw.Close()
	b := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil })
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, []v1.Layer{layer}...)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := name.ParseReference(w.registry + "/team/site:x")
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	d, _ := img.Digest()
	return w.registry + "/team/site@" + d.String()
}

func source(t *testing.T, ref, revision string) deploy.Source {
	c, err := appcontract.Parse([]byte("app:\n  web:\n    static: dist\n    public: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	return deploy.Source{Revision: revision, Contract: c, Images: map[string]string{"web": ref}, SelfServing: true}
}

func (w *world) placement() deploy.Placement {
	return deploy.Placement{ID: idstest.N(7), Name: "main", Generation: w.gen, Handle: w.handle}
}

func (w *world) logf(format string, args ...any) {
	w.logs = append(w.logs, strings.TrimSpace(strings.ReplaceAll(format, "%", "")))
	_ = args
}

// release renders and reconciles as the release path does, and settles a
// success into the deployment's handle as settlement does.
func (w *world) release(files map[string]string, revision string) (deploy.Observation, error) {
	d := w.driver()
	art, err := d.Render(context.Background(), w.placement(), source(w.t, w.image(files), revision), nil, nil, w.logf)
	if err != nil {
		return deploy.Observation{}, err
	}
	w.gen++
	o, err := d.Reconcile(context.Background(), w.placement(), w.gen, art, "k", w.logf, func(m map[string]any) error { w.noted = m; return nil })
	if err == nil {
		w.handle = o.Handle.(map[string]any)
	}
	return o, err
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

var v1Files = map[string]string{"index.html": "<h1>one</h1>", "assets/app-1.js": "one", "logo.png": "L"}

// The first release makes the deployment's bucket and records it before
// anything else touches it, sets its website, uploads assets, then entry
// files, then the marker, readable by anyone, and is witnessed at the
// bucket's website address. Listing the bucket anonymously is refused.
func TestTheFirstReleaseOwnsABucketAndPublishesInOrder(t *testing.T) {
	w := newWorld(t)
	o, err := w.release(v1Files, "abc")
	if err != nil {
		t.Fatal(err)
	}
	bucket := "apptown-game-site-main-aaaa0001"
	if !slices.Equal(w.owned, []string{bucket}) || o.Handle.(map[string]any)["bucket"] != bucket {
		t.Fatalf("owned %v, handle %v", w.owned, o.Handle)
	}
	var puts []string
	for _, c := range w.store.Calls() {
		if c.Method == "PUT" && c.Key != "" {
			puts = append(puts, c.Key)
		}
	}
	want := []string{"assets/app-1.js", "logo.png", "_apptown/env.js", "index.html", "_apptown/release"}
	if !slices.Equal(puts, want) {
		t.Fatalf("uploads %v, want %v", puts, want)
	}
	if c := w.store.Calls(); c[0].Method != "PUT" || c[0].Key != "" || c[1].Query != "website=" {
		t.Fatalf("the bucket is made, then its website set, before any upload: %+v", c[:2])
	}
	if status, body := get(t, o.URL+"/"); status != 200 || body != "<h1>one</h1>" {
		t.Fatalf("GET / = %d %q", status, body)
	}
	if status, body := get(t, o.URL+"/_apptown/release"); status != 200 || !strings.HasPrefix(body, "web@abc+") {
		t.Fatalf("marker = %d %q", status, body)
	}
	if obj, _ := w.store.Get(bucket, "index.html"); !obj.Public || obj.CacheControl != "no-store" {
		t.Fatalf("index.html = %+v", obj)
	}
	if obj, _ := w.store.Get(bucket, "assets/app-1.js"); !obj.Public || obj.CacheControl != "" {
		t.Fatalf("an asset = %+v", obj)
	}
	if status, _ := get(t, w.store.API.URL+"/"+bucket+"?list-type=2"); status != 403 {
		t.Fatalf("anonymous listing answered %d", status)
	}
	if w.noted["identity"] != o.Handle.(map[string]any)["identity"] {
		t.Fatalf("the identity is recorded before uploading: %v", w.noted)
	}
}

// Old files go one release late: after releases 1, 2 and 3, release 1's
// own files are gone and release 2's remain. A clean-up interrupted after
// release 3 is finished by release 4's listing. Every list and delete names
// the deployment's own bucket.
func TestOldFilesGoOneReleaseLate(t *testing.T) {
	w := newWorld(t)
	w.store.AddBucket("someone-elses")
	w.store.Put("someone-elses", "x", s3fake.Object{Body: []byte("x")})
	for i, files := range []map[string]string{
		{"index.html": "1", "assets/a-1.js": "1"},
		{"index.html": "2", "assets/a-2.js": "2"},
		{"index.html": "3", "assets/a-3.js": "3"},
	} {
		if _, err := w.release(files, "r"+string(rune('1'+i))); err != nil {
			t.Fatal(err)
		}
	}
	bucket := w.handle["bucket"].(string)
	keys := w.store.Keys(bucket)
	if slices.Contains(keys, "assets/a-1.js") || !slices.Contains(keys, "assets/a-2.js") || !slices.Contains(keys, "assets/a-3.js") {
		t.Fatalf("after three releases: %v", keys)
	}
	w.store.Fail("DELETE", bucket, "assets/a-2.js", 500)
	if _, err := w.release(map[string]string{"index.html": "4", "assets/a-4.js": "4"}, "r4"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(w.store.Keys(bucket), "assets/a-2.js") {
		t.Fatal("the failed delete leaves the file")
	}
	w.store.ClearFailures()
	if _, err := w.release(map[string]string{"index.html": "5", "assets/a-5.js": "5"}, "r5"); err != nil {
		t.Fatal(err)
	}
	if keys := w.store.Keys(bucket); slices.Contains(keys, "assets/a-2.js") || slices.Contains(keys, "assets/a-3.js") {
		t.Fatalf("release 5 finishes the clean-up: %v", keys)
	}
	for _, c := range w.store.Calls() {
		if (c.Method == "DELETE" || c.Method == "GET") && c.Bucket != bucket && c.Bucket != "" {
			t.Fatalf("a call on another bucket: %+v", c)
		}
	}
	if len(w.store.Keys("someone-elses")) != 1 {
		t.Fatal("another bucket is untouched")
	}
}

// An upload refused before the marker fails the release, names the
// incomplete publication, and leaves the address answering the previous
// marker.
func TestARefusedUploadIsAnIncompletePublication(t *testing.T) {
	w := newWorld(t)
	first, err := w.release(v1Files, "abc")
	if err != nil {
		t.Fatal(err)
	}
	bucket := w.handle["bucket"].(string)
	w.store.Fail("PUT", bucket, "index.html", 500)
	_, err = w.release(map[string]string{"index.html": "two", "assets/app-2.js": "2"}, "def")
	if !deploy.IsTerminal(err) || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("err = %v", err)
	}
	if _, body := get(t, first.URL+"/_apptown/release"); body != first.Handle.(map[string]any)["identity"] {
		t.Fatalf("the address answers %q, not the previous marker", body)
	}
}

// A publication the address does not answer yet (a cache, a slow host) is
// not a failure: the attempt is uncertain. Resume only witnesses — no
// upload, no delete — and settles it once the address answers.
func TestAnUnwitnessedPublicationIsUncertainAndResumeOnlyLooks(t *testing.T) {
	w := newWorld(t)
	if _, err := w.release(v1Files, "abc"); err != nil {
		t.Fatal(err)
	}
	w.store.Freeze()
	w.browser = map[string]string{"DSN": "changed"} // same revision, other values
	_, err := w.release(v1Files, "abc")
	if err == nil || deploy.IsTerminal(err) || !errors.Is(err, staticsite.ErrNotWitnessed) {
		t.Fatalf("err = %v", err)
	}
	before := len(w.store.Calls())
	w.store.Thaw()
	o, err := w.driver().Resume(context.Background(), w.placement(), w.gen, w.noted, w.logf)
	if err != nil {
		t.Fatal(err)
	}
	if o.Handle.(map[string]any)["identity"] != w.noted["identity"] {
		t.Fatalf("resume settles the recorded publication: %v", o.Handle)
	}
	if after := w.store.Calls(); len(after) != before {
		t.Fatalf("resume called the store: %+v", after[before:])
	}
}

// A taken bucket name is refused, never adopted: nothing is recorded or
// written to it.
func TestATakenBucketNameIsNeverAdopted(t *testing.T) {
	w := newWorld(t)
	w.store.AddBucket("apptown-game-site-main-aaaa0001")
	_, err := w.release(v1Files, "abc")
	if !deploy.IsTerminal(err) || !strings.Contains(err.Error(), "never adopts") {
		t.Fatalf("err = %v", err)
	}
	if len(w.owned) != 0 || len(w.store.Keys("apptown-game-site-main-aaaa0001")) != 0 {
		t.Fatal("the taken bucket was recorded or written to")
	}
}

// Take-down empties the bucket across listing pages, deletes it and sees
// the address stop answering. A failure says the site is still public and
// what is left.
func TestTakeDownDeletesTheBucketOrSaysWhatIsStillPublic(t *testing.T) {
	w := newWorld(t)
	w.store.PageSize = 2
	o, err := w.release(v1Files, "abc")
	if err != nil {
		t.Fatal(err)
	}
	bucket := w.handle["bucket"].(string)
	w.store.Fail("DELETE", bucket, "", 500)
	_, err = w.driver().Teardown(context.Background(), w.placement(), w.gen, deploy.TakeDown{})
	if err == nil || !strings.Contains(err.Error(), bucket) {
		t.Fatalf("a bucket that was not deleted: %v", err)
	}
	w.store.ClearFailures()
	if _, err := w.release(v1Files, "abc"); err != nil {
		t.Fatal(err)
	}
	w.store.Fail("DELETE", bucket, "logo.png", 500)
	_, err = w.driver().Teardown(context.Background(), w.placement(), w.gen, deploy.TakeDown{})
	if err == nil || !strings.Contains(err.Error(), "still public at "+o.URL) {
		t.Fatalf("a file that was not deleted: %v", err)
	}
	w.store.ClearFailures()
	// The bucket goes, but a website in an outage is not evidence that the
	// site stopped: the withdrawal stays unconfirmed, and the deployment.
	w.store.WebsiteAnswers(503)
	if _, err := w.driver().Teardown(context.Background(), w.placement(), w.gen, deploy.TakeDown{}); err == nil || !strings.Contains(err.Error(), "may still be public") {
		t.Fatalf("an unanswering website: %v", err)
	}
	w.store.WebsiteAnswers(0)
	taken, err := w.driver().Teardown(context.Background(), w.placement(), w.gen, deploy.TakeDown{})
	if err != nil || !slices.Equal(taken.Deleted, []string{"bucket " + bucket}) {
		t.Fatalf("take-down: %+v %v", taken, err)
	}
	if slices.Contains(w.store.Buckets(), bucket) {
		t.Fatal("the bucket is deleted")
	}
	if status, _ := get(t, o.URL+"/_apptown/release"); status == 200 {
		t.Fatal("the address stopped answering")
	}
}

// The key pair never reaches a log line, the artifact or an error.
func TestTheKeyPairIsNeverShown(t *testing.T) {
	w := newWorld(t)
	d := w.driver()
	art, err := d.Render(context.Background(), w.placement(), source(t, w.image(v1Files), "abc"), nil, nil, w.logf)
	if err != nil {
		t.Fatal(err)
	}
	w.store.Fail("PUT", "apptown-game-site-main-aaaa0001", "", 500)
	_, err = d.Reconcile(context.Background(), w.placement(), 1, art, "k", w.logf, func(map[string]any) error { return nil })
	text := strings.Join(w.logs, "\n") + string(art.Body) + err.Error()
	if strings.Contains(text, secretKey) || strings.Contains(text, accessKey) {
		t.Fatal("the key pair was shown")
	}
}

// Anything but a static-only app with an image by digest is refused.
func TestOnlyAStaticOnlyAppByDigestIsServed(t *testing.T) {
	w := newWorld(t)
	src := source(t, "registry.example/team/site:latest", "abc")
	if _, err := w.driver().Render(context.Background(), w.placement(), src, nil, nil, w.logf); !deploy.IsTerminal(err) {
		t.Fatalf("by tag: %v", err)
	}
	c, err := appcontract.Parse([]byte("app:\n  web: {image: app:1, public: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	src.Contract = c
	src.Images = map[string]string{"web": "registry.example/team/site@sha256:" + strings.Repeat("a", 64)}
	if _, err := w.driver().Render(context.Background(), w.placement(), src, nil, nil, w.logf); !deploy.IsTerminal(err) || !strings.Contains(err.Error(), "static-only") {
		t.Fatalf("a server part: %v", err)
	}
}

// Signing follows AWS's published Signature Version 4 example (GET Object,
// examplebucket, 24 May 2013).
func TestSigningMatchesTheAWSExample(t *testing.T) {
	c := &Client{Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	c.sign(req, sha256Hex(nil), time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); !strings.HasSuffix(got, want) || !strings.Contains(got, "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("Authorization = %s", got)
	}
}

// Observing asks the address: this publication's marker is healthy,
// another answer unhealthy.
func TestObservingAsksTheAddressForThisPublication(t *testing.T) {
	w := newWorld(t)
	if _, err := w.release(v1Files, "abc"); err != nil {
		t.Fatal(err)
	}
	o, err := w.driver().Observe(context.Background(), w.placement())
	if err != nil || o.Health != "healthy" {
		t.Fatalf("observed %+v, %v", o, err)
	}
	w.handle["identity"] = "web@abc+other"
	if o, err := w.driver().Observe(context.Background(), w.placement()); err != nil || o.Health != "unhealthy" {
		t.Fatalf("observed %+v, %v", o, err)
	}
}

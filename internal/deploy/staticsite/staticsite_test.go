package staticsite

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flokkit.io/forge/internal/oci"
)

var site = []oci.File{
	{Path: "index.html", Body: []byte("<script src=/assets/app-1.js></script>")},
	{Path: "assets/app-1.js", Body: []byte("js")},
	{Path: "about/index.htm", Body: []byte("about")},
	{Path: "_apptown/release", Body: []byte("a file of the site's own")},
}

// A set uploads assets, then entry files and the browser values, then the
// marker last; the site's own file of an AppTown name is replaced.
func TestASetPutsAssetsThenEntriesThenTheMarkerLast(t *testing.T) {
	set, err := Publish(site, "web@abc", "sha256:1", map[string]string{"DSN": "x"})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, f := range set.Files {
		order = append(order, f.Key)
	}
	if got := strings.Join(order, " "); got != "assets/app-1.js _apptown/env.js about/index.htm index.html _apptown/release" {
		t.Fatalf("order = %s", got)
	}
	last := set.Files[len(set.Files)-1]
	if string(last.Body) != set.Identity || !strings.HasPrefix(set.Identity, "web@abc+") {
		t.Fatalf("marker %q, identity %q", last.Body, set.Identity)
	}
	if env := string(set.Files[1].Body); !strings.Contains(env, `{"DSN":"x"}`) {
		t.Fatalf("env.js = %s", env)
	}
	if set.Files[0].NoStore() || !set.Files[3].NoStore() || !last.NoStore() {
		t.Fatal("only entry files and AppTown's files are no-store")
	}
}

// The identity names the image and the browser values together: the same
// revision with other values is another publication, the same inputs the
// same one; no values read as {}.
func TestTheIdentityNamesTheImageAndTheValues(t *testing.T) {
	id := func(digest string, v map[string]string) string {
		s, err := Identity("web@abc", digest, v)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := id("sha256:1", map[string]string{"A": "1", "B": "2"})
	if a != id("sha256:1", map[string]string{"B": "2", "A": "1"}) {
		t.Fatal("the same values in another order are the same publication")
	}
	if a == id("sha256:1", map[string]string{"A": "1", "B": "3"}) || a == id("sha256:2", map[string]string{"A": "1", "B": "2"}) {
		t.Fatal("other values or another image must be another publication")
	}
	if id("sha256:1", nil) != id("sha256:1", map[string]string{}) {
		t.Fatal("no values is {}")
	}
}

// The witness passes only on exactly this identity; an older marker or a
// same-revision marker with other values is not witnessed, and every
// request asks past caches.
func TestTheWitnessWantsExactlyThisPublication(t *testing.T) {
	var answer atomic.Value
	answer.Store("web@abc+old")
	var cacheBusted atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cacheBusted.Store(r.URL.Query().Get("forge-witness") != "" && r.Header.Get("Cache-Control") == "no-cache")
		w.Write([]byte(answer.Load().(string) + "\n"))
	}))
	defer srv.Close()
	logf := func(string, ...any) {}
	err := Witness(context.Background(), srv.URL, "web@abc+new", 2, time.Millisecond, logf)
	if !errors.Is(err, ErrNotWitnessed) || !strings.Contains(err.Error(), "web@abc+old") {
		t.Fatalf("old marker: %v", err)
	}
	if !cacheBusted.Load() {
		t.Fatal("the witness must ask past caches")
	}
	answer.Store("web@abc+new")
	if err := Witness(context.Background(), srv.URL, "web@abc+new", 2, time.Millisecond, logf); err != nil {
		t.Fatal(err)
	}
	if err := Gone(context.Background(), srv.URL, 2, time.Millisecond); err == nil {
		t.Fatal("an address still answering its marker is not gone")
	}
}

// Only the host's answer that it serves nothing there (404) is absence. A
// server error or no answer at all leaves the withdrawal unknown.
func TestOnlyA404IsGone(t *testing.T) {
	for _, status := range []int{500, 502, 503, 403} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		if err := Gone(context.Background(), srv.URL, 2, time.Millisecond); err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("%d: %v", status, err)
		}
		srv.Close()
		if status == 403 {
			if err := Gone(context.Background(), srv.URL, 2, time.Millisecond); err == nil {
				t.Fatal("a closed server is not evidence of absence")
			}
		}
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	defer gone.Close()
	if err := Gone(context.Background(), gone.URL, 2, time.Millisecond); err != nil {
		t.Fatalf("a 404 is gone: %v", err)
	}
}

// A marker body that could not be read whole is not the marker, even when
// the part that arrived matches: a truncated body, and one that stalls
// after the identity, are both unwitnessed.
func TestAMarkerBodyThatIsNotReadWholeIsNoAnswer(t *testing.T) {
	truncated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("web@abc+new"))
	}))
	defer truncated.Close()
	if err := Witness(context.Background(), truncated.URL, "web@abc+new", 2, time.Millisecond, func(string, ...any) {}); !errors.Is(err, ErrNotWitnessed) {
		t.Fatalf("a truncated body: %v", err)
	}
	if _, _, err := Answer(context.Background(), truncated.URL); err == nil {
		t.Fatal("a truncated body is an answer")
	}
	release := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("web@abc+new"))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer stalled.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := Witness(ctx, stalled.URL, "web@abc+new", 1, time.Millisecond, func(string, ...any) {}); err == nil {
		t.Fatal("a body that stalls after the identity was witnessed")
	}
}

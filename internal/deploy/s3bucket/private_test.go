package s3bucket

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/s3bucket/s3fake"
)

// A backup store's object is written private, never with the website's
// public-read; what the store then holds is checked against the file sent
// (its size and MD5), and a store that kept other bytes is an error, not a
// stored copy. Reading it back gives the same bytes and digest.
func TestAPrivateFileIsWrittenPrivateAndCheckedWhereItLands(t *testing.T) {
	t.Parallel()
	store := s3fake.New(t, "AKID")
	store.AddBucket("copies")
	c := &Client{Endpoint: store.API.URL, Region: "syd1", AccessKey: "AKID", SecretKey: "secret"}
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "postgres.dump")
	body := bytes.Repeat([]byte("ledger 41 42\n"), 1000)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.PutPrivateFile(ctx, "copies", "sets/1/postgres.dump", path)
	if err != nil || got.Size != int64(len(body)) || len(got.SHA256) != 64 {
		t.Fatalf("put: %+v %v", got, err)
	}
	o, ok := store.Get("copies", "sets/1/postgres.dump")
	if !ok || o.ACL != "private" || o.Public || o.SHA256 != got.SHA256 {
		t.Fatalf("stored: acl %q public %v sha %q", o.ACL, o.Public, o.SHA256)
	}
	back := filepath.Join(dir, "back.dump")
	n, sum, err := c.GetToFile(ctx, "copies", "sets/1/postgres.dump", back)
	if err != nil || n != got.Size || sum != got.SHA256 {
		t.Fatalf("read back: %d %s %v", n, sum, err)
	}

	// A store that keeps fewer bytes, or other bytes of the same size.
	for name, mangle := range map[string]func(string, []byte) []byte{
		"short":   func(_ string, b []byte) []byte { return b[:len(b)-1] },
		"altered": func(_ string, b []byte) []byte { c := bytes.Clone(b); c[0] ^= 1; return c },
	} {
		store.Mangle = mangle
		if _, err := c.PutPrivateFile(ctx, "copies", "sets/2/"+name, path); err == nil || !strings.Contains(err.Error(), "store") {
			t.Errorf("%s: a store that kept other bytes was taken as a stored copy: %v", name, err)
		}
	}
	store.Mangle = nil
	if _, err := c.Head(ctx, "copies", "sets/3/none"); !IsCode(err, "NoSuchKey") {
		t.Fatalf("head of nothing: %v", err)
	}
	if err := c.PutPrivate(ctx, "copies", "sets/1/manifest.json", "application/json", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if b, err := c.GetObject(ctx, "copies", "sets/1/manifest.json"); err != nil || string(b) != "{}" {
		t.Fatalf("manifest: %q %v", b, err)
	}
	if o, _ := store.Get("copies", "sets/1/manifest.json"); o.ACL != "private" {
		t.Fatalf("manifest acl: %q", o.ACL)
	}
}

// A file larger than one part goes up in parts, in order, and the store
// holds it whole under a multipart ETag that is checked. A part the store
// refuses aborts the upload, so the store keeps no parts and no object.
//
// Kills: a file over one request's limit refused or sent whole; parts
// joined out of order or one dropped; an abandoned upload left behind.
func TestALargeFileGoesUpInPartsAndAFailedPartAbortsTheUpload(t *testing.T) {
	t.Parallel()
	store := s3fake.New(t, "AKID")
	store.AddBucket("copies")
	c := &Client{Endpoint: store.API.URL, Region: "syd1", AccessKey: "AKID", SecretKey: "secret"}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "apptown.backup")
	var body []byte
	for i := range 2500 {
		body = append(body, byte(i%251))
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.putPrivateFile(ctx, "copies", "apptown/a.backup", path, 1024)
	if err != nil || got.Size != int64(len(body)) {
		t.Fatalf("put: %+v %v", got, err)
	}
	o, ok := store.Get("copies", "apptown/a.backup")
	if !ok || !bytes.Equal(o.Body, body) || !strings.HasSuffix(o.ETag, "-3") || o.ACL != "private" || o.SHA256 != got.SHA256 {
		t.Fatalf("stored: %d bytes, etag %q, acl %q", len(o.Body), o.ETag, o.ACL)
	}
	parts := 0
	for _, call := range store.Calls() {
		if call.Method == "PUT" && strings.Contains(call.Query, "partNumber=") {
			parts++
		}
	}
	if parts != 3 {
		t.Fatalf("%d parts sent", parts)
	}

	store.Fail("PUT", "copies", "apptown/b.backup", 500)
	if _, err := c.putPrivateFile(ctx, "copies", "apptown/b.backup", path, 1024); err == nil {
		t.Fatal("a refused part was taken as a stored copy")
	}
	if n := store.OpenUploads(); n != 0 {
		t.Fatalf("%d uploads left open", n)
	}
	if _, ok := store.Get("copies", "apptown/b.backup"); ok {
		t.Fatal("a failed upload left an object")
	}
}

// What a successful upload says it stored is what the store holds: a file
// that changes while it goes up is not completed, and a completion the
// store answers 200 with an error in its body is a failure. Both abort.
//
// Kills: a stored copy whose recorded SHA-256 describes bytes it does not
// hold; a 200 taken as a completed upload.
func TestAnUploadThatCannotBeTrustedIsAborted(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"changed", "error in a 200"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			store := s3fake.New(t, "AKID")
			store.AddBucket("copies")
			path := filepath.Join(t.TempDir(), "archive")
			if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			endpoint := store.API.URL
			switch scenario {
			case "changed":
				store.SetBefore(func(call s3fake.Call) {
					if call.Method == "POST" && call.Query == "uploads=" {
						if err := os.WriteFile(path, []byte("after!"), 0o600); err != nil {
							t.Error(err)
						}
					}
				})
			case "error in a 200":
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" && r.URL.Query().Has("uploadId") {
						fmt.Fprint(w, " \n<Error><Code>InternalError</Code><Message>completion failed</Message></Error>")
						return
					}
					store.API.Config.Handler.ServeHTTP(w, r)
				}))
				t.Cleanup(api.Close)
				endpoint = api.URL
			}
			c := &Client{Endpoint: endpoint, Region: "syd1", AccessKey: "AKID", SecretKey: "secret"}
			if got, err := c.PutPrivateFile(context.Background(), "copies", "archive", path); err == nil {
				t.Fatalf("taken as stored: %+v", got)
			}
			if n := store.OpenUploads(); n != 0 {
				t.Fatalf("%d uploads left open", n)
			}
			if _, ok := store.Get("copies", "archive"); ok {
				t.Fatal("the store holds the object")
			}
		})
	}
}

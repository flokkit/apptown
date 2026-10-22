package s3bucket

import (
	"bytes"
	"context"
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

package backuparchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

const pass = "correct horse battery"

// A second of scrypt per archive buys these tests nothing.
func init() { workFactor = 10 }

func write(t *testing.T, add func(w *writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, pass)
	if err != nil {
		t.Fatal(err)
	}
	add(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// extractAll reads every entry into dir and finishes the stream.
func extractAll(t *testing.T, archive []byte, passphrase, dir string) (map[string]Digest, error) {
	t.Helper()
	r, err := NewReader(bytes.NewReader(archive), passphrase)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	got := map[string]Digest{}
	for {
		hdr, err := r.Next()
		if err == io.EOF {
			return got, r.Finish()
		}
		if err != nil {
			return got, err
		}
		d, err := r.Extract(root, hdr, "data/")
		if err != nil {
			return got, err
		}
		got[hdr.Name] = d
	}
}

func TestATreeComesBackWithItsFilesModesAndLinks(t *testing.T) {
	src := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(src, "ssh"), 0o700))
	must(t, os.WriteFile(filepath.Join(src, "ssh", "id"), []byte("key"), 0o600))
	must(t, os.WriteFile(filepath.Join(src, "secrets.env"), []byte("A=1\n"), 0o640))
	must(t, os.MkdirAll(filepath.Join(src, "cache", "x"), 0o755))
	must(t, os.WriteFile(filepath.Join(src, "cache", "x", "big"), []byte("skip me"), 0o644))
	must(t, os.Symlink("secrets.env", filepath.Join(src, "link")))
	var want Digest
	archive := write(t, func(w *writer) {
		var err error
		if want, err = w.File("data/secrets.env", filepath.Join(src, "secrets.env")); err != nil {
			t.Fatal(err)
		}
		if err := w.Tree("data", src, func(rel string) bool { return rel == "cache" || rel == "secrets.env" }); err != nil {
			t.Fatal(err)
		}
	})
	dst := t.TempDir()
	got, err := extractAll(t, archive, pass, dst)
	if err != nil {
		t.Fatal(err)
	}
	if got["data/secrets.env"] != want || want.Bytes != 4 {
		t.Fatalf("digest of secrets.env = %+v, want %+v", got["data/secrets.env"], want)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "ssh", "id")); string(b) != "key" {
		t.Fatalf("ssh/id = %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "ssh", "id")); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("ssh/id mode = %v", fi)
	}
	if fi, _ := os.Stat(filepath.Join(dst, "secrets.env")); fi == nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("secrets.env mode = %v", fi)
	}
	if l, _ := os.Readlink(filepath.Join(dst, "link")); l != "secrets.env" {
		t.Fatalf("link = %q", l)
	}
	if _, err := os.Stat(filepath.Join(dst, "cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a skipped directory came back: %v", err)
	}
}

func TestAnotherPassphraseIsRefusedByName(t *testing.T) {
	archive := write(t, func(w *writer) { must(t, w.Bytes("manifest.json", []byte("{}"))) })
	if _, err := NewReader(bytes.NewReader(archive), "not the passphrase"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("err = %v, want ErrPassphrase", err)
	}
}

func TestAShortPassphraseIsRefused(t *testing.T) {
	if _, err := NewWriter(io.Discard, "short"); err == nil {
		t.Fatal("a 5-character passphrase was accepted")
	}
}

// Kills: reading entries to the tar's end and calling that a whole archive
// when its encrypted end was cut off.
func TestACutOffArchiveFails(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 200<<10)
	archive := write(t, func(w *writer) { must(t, w.Bytes("data/f", big)) })
	for _, n := range []int{len(archive) - 1, len(archive) - 100, len(archive) / 2} {
		if _, err := extractAll(t, archive[:n], pass, t.TempDir()); err == nil {
			t.Fatalf("an archive cut to %d of %d bytes read as whole", n, len(archive))
		}
	}
}

func TestAChangedByteFails(t *testing.T) {
	archive := write(t, func(w *writer) { must(t, w.Bytes("data/f", bytes.Repeat([]byte("y"), 100<<10))) })
	archive[len(archive)/2] ^= 1
	if _, err := extractAll(t, archive, pass, t.TempDir()); err == nil {
		t.Fatal("a changed archive read as whole")
	}
}

// sealed builds an archive by hand, for entries Writer never makes.
func sealed(t *testing.T, hdrs ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	r, _ := age.NewScryptRecipient(pass)
	r.SetWorkFactor(workFactor)
	enc, err := age.Encrypt(&buf, r)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	for _, h := range hdrs {
		must(t, tw.WriteHeader(h))
		if h.Size > 0 {
			_, err := tw.Write(bytes.Repeat([]byte("z"), int(h.Size)))
			must(t, err)
		}
	}
	must(t, tw.Close())
	must(t, gz.Close())
	must(t, enc.Close())
	return buf.Bytes()
}

func TestNoEntryWritesOutsideTheDirectory(t *testing.T) {
	cases := map[string][]*tar.Header{
		"dot-dot":       {{Typeflag: tar.TypeReg, Name: "data/../escaped", Size: 1, Mode: 0o600}},
		"absolute":      {{Typeflag: tar.TypeReg, Name: "data//etc/escaped", Size: 1, Mode: 0o600}},
		"through link":  {{Typeflag: tar.TypeSymlink, Name: "data/out", Linkname: "..", Mode: 0o777}, {Typeflag: tar.TypeReg, Name: "data/out/escaped", Size: 1, Mode: 0o600}},
		"absolute link": {{Typeflag: tar.TypeSymlink, Name: "data/out", Linkname: "/tmp", Mode: 0o777}, {Typeflag: tar.TypeReg, Name: "data/out/escaped", Size: 1, Mode: 0o600}},
		"hard link":     {{Typeflag: tar.TypeLink, Name: "data/h", Linkname: "/etc/passwd"}},
	}
	for name, hdrs := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "in")
			must(t, os.Mkdir(dir, 0o700))
			_, err := extractAll(t, sealed(t, hdrs...), pass, dir)
			if err == nil {
				t.Fatal("extracted")
			}
			entries, _ := os.ReadDir(parent)
			for _, e := range entries {
				if e.Name() != "in" {
					t.Fatalf("%s was written beside the directory", e.Name())
				}
			}
			if _, err := os.Stat("/tmp/escaped"); err == nil && !strings.HasPrefix(dir, "/tmp/") {
				t.Fatal("/tmp/escaped was written")
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

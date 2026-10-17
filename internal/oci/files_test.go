package oci

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// entry is one tar entry of a test layer: a folder (trailing /), a file,
// or a link (link set).
type entry struct {
	name, body, link string
	hard             bool
}

func layerOf(t *testing.T, entries ...entry) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644}
		switch {
		case strings.HasSuffix(e.name, "/"):
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		case e.link != "" && e.hard:
			h.Typeflag, h.Linkname = tar.TypeLink, e.link
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil })
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// pushLayers writes an image of the layers to the registry and answers it
// by digest.
func pushLayers(t *testing.T, host string, layers ...v1.Layer) string {
	t.Helper()
	img, err := mutate.AppendLayers(empty.Image, layers...)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(host + "/team/site:t")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return host + "/team/site@" + d.String()
}

// A static site's files come out of its image as the layers leave them:
// later layers win, whiteouts remove, and only what is under the folder
// asked for is read, sorted, relative to it.
func TestReadTreeReadsTheFolderAsTheLayersLeaveIt(t *testing.T) {
	host := newRegistry(t, nil)
	ref := pushLayers(t, host,
		layerOf(t, entry{name: "etc/passwd", body: "root"}, entry{name: "srv/"}, entry{name: "srv/index.html", body: "old"}, entry{name: "srv/gone.txt", body: "x"}),
		layerOf(t, entry{name: "srv/index.html", body: "new"}, entry{name: "srv/assets/"}, entry{name: "srv/assets/app-1.js", body: "js"}, entry{name: "srv/.wh.gone.txt"}))
	files, err := ReadTree(context.Background(), Source{Reference: ref}, "/srv", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path+"="+string(f.Body))
	}
	if strings.Join(got, " ") != "assets/app-1.js=js index.html=new" {
		t.Fatalf("files = %v", got)
	}
}

// Nothing outside the folder can be read through it: a symbolic link or a
// hard link under it is refused by name, wherever it points.
func TestReadTreeRefusesLinksUnderTheFolder(t *testing.T) {
	host := newRegistry(t, nil)
	for _, link := range []entry{
		{name: "srv/passwd", link: "/etc/passwd"},
		{name: "srv/passwd", link: "etc/passwd", hard: true},
	} {
		ref := pushLayers(t, host, layerOf(t, entry{name: "etc/passwd", body: "root"}, entry{name: "srv/"}, link))
		_, err := ReadTree(context.Background(), Source{Reference: ref}, "/srv", 1<<20)
		if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "/srv/passwd") {
			t.Fatalf("link %+v: err = %v", link, err)
		}
	}
}

// A folder the image does not have, files beyond the limit, and a
// reference by tag are each refused with their own class.
func TestReadTreeRefusesAMissingFolderTooMuchAndATag(t *testing.T) {
	host := newRegistry(t, nil)
	ref := pushLayers(t, host, layerOf(t, entry{name: "srv/"}, entry{name: "srv/a", body: "12345"}, entry{name: "srv/b", body: "67890"}))
	if _, err := ReadTree(context.Background(), Source{Reference: ref}, "/www", 1<<20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing folder: %v", err)
	}
	if _, err := ReadTree(context.Background(), Source{Reference: ref}, "/srv", 9); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	if _, err := ReadTree(context.Background(), Source{Reference: ref}, "/srv", 10); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	if _, err := ReadTree(context.Background(), Source{Reference: host + "/team/site:t"}, "/srv", 1<<20); !errors.Is(err, ErrMalformed) {
		t.Fatalf("by tag: %v", err)
	}
}

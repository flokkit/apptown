package oci

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeTar packs files into a tar as `docker save` does: folders first,
// then each file, with oci-layout and manifest.json beside the layout.
func writeTar(t *testing.T, files layoutFiles) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := tar.NewWriter(f)
	for _, dir := range []string{"blobs/", "blobs/sha256/"} {
		if err := w.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	all := layoutFiles{"oci-layout": []byte(`{"imageLayoutVersion": "1.0.0"}`), "manifest.json": []byte(`[]`)}
	for name, b := range files {
		all[name] = b
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(all[name]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(all[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// An image `docker save` packed in a tar publishes as the same layout
// would: the registry answers the manifest's digest. A tar that lacks a
// blob fails as malformed and nothing is tagged.
func TestAnImageSavedToATarPublishesByItsDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	img := newImage(t, "amd64", "layer one", "layer two")

	repo := newRegistry(t, nil) + "/team/app"
	l, err := OpenTarLayout(writeTar(t, img.files))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got, err := Publish(ctx, l, Repository{Name: repo}, "change-1-part-web", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != img.digest() {
		t.Fatalf("published %s, want %s", got.Digest, img.digest())
	}

	delete(img.files, img.layers[1])
	repo = newRegistry(t, nil) + "/team/app"
	short, err := OpenTarLayout(writeTar(t, img.files))
	if err != nil {
		t.Fatal(err)
	}
	defer short.Close()
	if _, err := Publish(ctx, short, Repository{Name: repo}, "change-1-part-web", "linux/amd64"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a tar without a layer published: %v", err)
	}
	if got := tags(t, repo); len(got) != 0 {
		t.Fatalf("a tar without a layer left tags %v", got)
	}
}

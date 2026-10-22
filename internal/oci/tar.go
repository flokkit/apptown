package oci

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
)

// TarLayout is an OCI image layout packed in a tar file on disk, as `docker
// save` writes one (Docker 25 and later): index.json, oci-layout and
// blobs/sha256/<hex>. Its regular files are found once, by one read of the
// tar; Open serves each from its place in the file. Publish verifies every
// blob it reads, so the tar is not trusted for its content. Close the
// layout when done.
type TarLayout struct {
	f     *os.File
	files map[string]tarFile
}

type tarFile struct{ offset, size int64 }

// OpenTarLayout reads the tar at path's table of files.
func OpenTarLayout(path string) (*TarLayout, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	l := &TarLayout{f: f, files: map[string]tarFile{}}
	if err := l.index(); err != nil {
		f.Close()
		return nil, err
	}
	return l, nil
}

func (l *TarLayout) index() error {
	r := tar.NewReader(l.f)
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: the image's tar: %w", ErrMalformed, err)
		}
		if h.Typeflag != tar.TypeReg {
			continue // folders, and links no layout file needs
		}
		// The reader stands at the file's content: its offset is where it
		// lies in the tar.
		at, err := l.f.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		l.files[path.Clean(h.Name)] = tarFile{offset: at, size: h.Size}
	}
}

// Open serves one file of the layout; a file it does not have is
// fs.ErrNotExist.
func (l *TarLayout) Open(_ context.Context, name string) (io.ReadCloser, error) {
	file, ok := l.files[path.Clean(name)]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(io.NewSectionReader(l.f, file.offset, file.size)), nil
}

func (l *TarLayout) Close() error { return l.f.Close() }

var _ Layout = (*TarLayout)(nil)

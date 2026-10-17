package oci

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ErrTooLarge: the files asked for are more than the caller allows.
var ErrTooLarge = errors.New("too large")

// File is one regular file read out of an image's filesystem: its path
// relative to the folder asked for, its permission bits and its bytes.
type File struct {
	Path string
	Mode int64
	Body []byte
}

// ReadTree reads every regular file under root (an absolute folder such
// as /srv) in the image from names by digest, as the image's layers
// leave the filesystem (later layers win, whiteouts remove). It is how a
// static site's files are taken out of its release image without running
// it.
//
// Under root only folders and regular files are accepted: a symbolic
// link, a hard link or any other entry is refused, naming its path, so
// nothing outside the folder can be read through it. Entries outside root
// are not read. A root the image does not have is ErrNotFound; files
// whose bytes add up to more than limit are ErrTooLarge. The reference
// must name a digest, and an index is refused (one image, one platform).
func ReadTree(ctx context.Context, from Source, root string, limit int64) ([]File, error) {
	ref, err := name.NewDigest(from.Reference, name.StrictValidation)
	if err != nil {
		return nil, fmt.Errorf("%w: %q must name an image by digest: %w", ErrMalformed, from.Reference, err)
	}
	desc, err := remote.Get(ref, options(ctx, from.Credential)...)
	if err != nil {
		return nil, classify("reading "+ref.String(), err, ErrMalformed)
	}
	if desc.MediaType.IsIndex() {
		return nil, fmt.Errorf("%w: %s is an index, not one image", ErrUnsupported, ref)
	}
	img, err := desc.Image()
	if err != nil {
		return nil, classify("reading "+ref.String(), err, ErrMalformed)
	}
	root = strings.Trim(path.Clean("/"+root), "/")
	rc := mutate.Extract(img)
	defer rc.Close()
	tr := tar.NewReader(rc)
	var files []File
	var total int64
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, classify("reading the layers of "+ref.String(), err, ErrMalformed)
		}
		p := strings.Trim(path.Clean("/"+h.Name), "/")
		rel, under := strings.CutPrefix(p, root+"/")
		if p == root {
			if h.Typeflag != tar.TypeDir {
				return nil, fmt.Errorf("%w: /%s in %s is not a folder", ErrUnsupported, root, ref)
			}
			found = true
			continue
		}
		if !under {
			continue
		}
		found = true
		switch h.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg:
			if total += h.Size; total > limit {
				return nil, fmt.Errorf("%w: the files under /%s add up to more than %d bytes", ErrTooLarge, root, limit)
			}
			body, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
			if err != nil {
				return nil, classify("reading /"+p, err, ErrMalformed)
			}
			if int64(len(body)) != h.Size {
				return nil, fmt.Errorf("%w: /%s is %d bytes, its header says %d", ErrMalformed, p, len(body), h.Size)
			}
			files = append(files, File{Path: rel, Mode: h.Mode & 0o777, Body: body})
		default:
			return nil, fmt.Errorf("%w: /%s is not a plain file or folder (a link or a special file)", ErrUnsupported, p)
		}
	}
	if !found {
		return nil, fmt.Errorf("%w: %s has no /%s", ErrNotFound, ref, root)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Layout is an OCI image layout read file by file: "index.json" and
// "blobs/sha256/<hex>". Open may be called concurrently. A file the layout
// does not have is fs.ErrNotExist.
type Layout interface {
	Open(ctx context.Context, name string) (io.ReadCloser, error)
}

// maxDocument bounds the JSON documents read into memory (index, manifest,
// config): registries refuse manifests larger than this too.
const maxDocument = 4 << 20

var hexDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Publish writes the one image in l to repository to, tagged tag, and
// returns it as the registry answers for its digest. The image must be one
// OCI image manifest whose config names platform. Every blob read from l
// must have its descriptor's size and digest; one that does not fails the
// publication before the manifest is written, so nothing is tagged. Blobs
// the registry already has are not sent again.
func Publish(ctx context.Context, l Layout, to Repository, tag, platform string) (Image, error) {
	repo, err := repositoryOf(to.Name)
	if err != nil {
		return Image{}, err
	}
	ref, err := name.NewTag(repo.Name()+":"+tag, name.StrictValidation)
	if err != nil {
		return Image{}, fmt.Errorf("%w: tag %q: %w", ErrMalformed, tag, err)
	}
	img, err := readLayout(ctx, l, platform)
	if err != nil {
		return Image{}, err
	}
	v, err := partial.CompressedToImage(img)
	if err != nil {
		return Image{}, err
	}
	err = remote.Write(ref, v, options(ctx, to.Credential)...)
	if bad := img.failure(); bad != nil {
		return Image{}, bad // whatever the transfer made of it
	}
	if err != nil {
		return Image{}, classify("publishing "+ref.String(), err, nil)
	}
	return confirmAs(ctx, to, img.descriptor)
}

// layoutImage is a layout's one image: its manifest and config already read
// and checked, its layers streamed from the layout on demand.
type layoutImage struct {
	ctx        context.Context
	layout     Layout
	descriptor v1.Descriptor
	manifest   []byte
	config     []byte
	layers     map[v1.Hash]v1.Descriptor

	mu  sync.Mutex
	bad error
}

func readLayout(ctx context.Context, l Layout, platform string) (*layoutImage, error) {
	raw, err := readAll(ctx, l, "index.json")
	if err != nil {
		return nil, err
	}
	var index v1.IndexManifest
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("%w: index.json: %w", ErrMalformed, err)
	}
	if len(index.Manifests) != 1 {
		return nil, fmt.Errorf("%w: index.json names %d manifests, not one image", ErrUnsupported, len(index.Manifests))
	}
	d := index.Manifests[0]
	switch {
	case d.Annotations["vnd.docker.reference.type"] == "attestation-manifest":
		return nil, fmt.Errorf("%w: index.json names an attestation manifest", ErrUnsupported)
	case d.MediaType.IsIndex():
		return nil, fmt.Errorf("%w: index.json names an index of images (%s), not one image", ErrUnsupported, d.MediaType)
	case d.MediaType != types.OCIManifestSchema1:
		return nil, fmt.Errorf("%w: index.json names a %q, not an OCI image manifest", ErrUnsupported, d.MediaType)
	}
	img := &layoutImage{ctx: ctx, layout: l, descriptor: d, layers: map[v1.Hash]v1.Descriptor{}}
	if img.manifest, err = img.document(d); err != nil {
		return nil, err
	}
	var m v1.Manifest
	if err := json.Unmarshal(img.manifest, &m); err != nil {
		return nil, fmt.Errorf("%w: the manifest: %w", ErrMalformed, err)
	}
	if m.MediaType != "" && m.MediaType != d.MediaType {
		return nil, fmt.Errorf("%w: the manifest says it is a %q, index.json a %q", ErrMalformed, m.MediaType, d.MediaType)
	}
	if m.Config.MediaType != types.OCIConfigJSON {
		return nil, fmt.Errorf("%w: the manifest's config is a %q, not an image config", ErrUnsupported, m.Config.MediaType)
	}
	if img.config, err = img.document(m.Config); err != nil {
		return nil, err
	}
	var cfg v1.ConfigFile
	if err := json.Unmarshal(img.config, &cfg); err != nil {
		return nil, fmt.Errorf("%w: the config: %w", ErrMalformed, err)
	}
	got, err := platformOf(cfg.OS, cfg.Architecture, cfg.Variant)
	if err != nil {
		return nil, err
	}
	if got != platform {
		return nil, fmt.Errorf("%w: the image is for %s, not %s", ErrUnsupported, got, platform)
	}
	for _, layer := range m.Layers {
		if !strings.HasPrefix(string(layer.MediaType), "application/vnd.oci.image.layer.v1.") || !layer.MediaType.IsDistributable() {
			return nil, fmt.Errorf("%w: a layer is a %q", ErrUnsupported, layer.MediaType)
		}
		if _, err := blobPath(layer); err != nil {
			return nil, err
		}
		img.layers[layer.Digest] = layer
	}
	return img, nil
}

// document reads a descriptor's whole blob into memory, checked.
func (img *layoutImage) document(d v1.Descriptor) ([]byte, error) {
	if d.Size < 0 || d.Size > maxDocument {
		return nil, fmt.Errorf("%w: a %d-byte %s", ErrUnsupported, d.Size, d.MediaType)
	}
	r, err := img.open(d)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if bad := img.failure(); bad != nil {
		return nil, bad
	}
	return b, err
}

func blobPath(d v1.Descriptor) (string, error) {
	if d.Digest.Algorithm != "sha256" {
		return "", fmt.Errorf("%w: digest algorithm %q", ErrUnsupported, d.Digest.Algorithm)
	}
	if !hexDigest.MatchString(d.Digest.Hex) {
		return "", fmt.Errorf("%w: digest %q", ErrMalformed, d.Digest)
	}
	if d.Size < 0 {
		return "", fmt.Errorf("%w: blob %s has size %d", ErrMalformed, d.Digest, d.Size)
	}
	return "blobs/sha256/" + d.Digest.Hex, nil
}

// open streams a descriptor's blob from the layout, verified as it is read.
func (img *layoutImage) open(d v1.Descriptor) (io.ReadCloser, error) {
	path, err := blobPath(d)
	if err != nil {
		return nil, err
	}
	r, err := img.layout.Open(img.ctx, path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, img.fail(fmt.Errorf("%w: the layout has no blob %s", ErrMalformed, d.Digest))
	} else if err != nil {
		return nil, img.fail(fmt.Errorf("reading blob %s: %w", d.Digest, err))
	}
	return &verified{ReadCloser: r, want: d.Digest.Hex, size: d.Size, sum: sha256.New(), fail: img.fail}, nil
}

// fail records the first failure to read or verify a blob from the
// layout: the publication fails with it however the transfer that met it
// reports it, and a layout that cannot be read is never taken for a
// registry that cannot be reached.
func (img *layoutImage) fail(err error) error {
	img.mu.Lock()
	defer img.mu.Unlock()
	if img.bad == nil {
		img.bad = err
	}
	return err
}

func (img *layoutImage) failure() error {
	img.mu.Lock()
	defer img.mu.Unlock()
	return img.bad
}

func readAll(ctx context.Context, l Layout, path string) ([]byte, error) {
	r, err := l.Open(ctx, path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: the layout has no %s", ErrMalformed, path)
	} else if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, maxDocument+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(b) > maxDocument {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrUnsupported, path, maxDocument)
	}
	return b, nil
}

// The CompressedImageCore the library writes from.

func (img *layoutImage) RawConfigFile() ([]byte, error) { return img.config, nil }
func (img *layoutImage) RawManifest() ([]byte, error)   { return img.manifest, nil }
func (img *layoutImage) MediaType() (types.MediaType, error) {
	return img.descriptor.MediaType, nil
}
func (img *layoutImage) LayerByDigest(h v1.Hash) (partial.CompressedLayer, error) {
	d, ok := img.layers[h]
	if !ok {
		return nil, fmt.Errorf("%w: the manifest names no layer %s", ErrMalformed, h)
	}
	return &layoutLayer{img: img, d: d}, nil
}

type layoutLayer struct {
	img *layoutImage
	d   v1.Descriptor
}

func (l *layoutLayer) Digest() (v1.Hash, error)            { return l.d.Digest, nil }
func (l *layoutLayer) Size() (int64, error)                { return l.d.Size, nil }
func (l *layoutLayer) MediaType() (types.MediaType, error) { return l.d.MediaType, nil }
func (l *layoutLayer) Compressed() (io.ReadCloser, error)  { return l.img.open(l.d) }

// verified reads a blob that must be exactly size bytes with the given
// sha256. The last bytes are handed over only after the digest matches and
// the source is confirmed to end there: a reader that stops at the declared
// length (an HTTP body with its Content-Length) never receives a whole
// blob that is wrong. A short, long or mismatched blob is an error, never
// io.EOF.
type verified struct {
	io.ReadCloser
	want       string
	size, read int64
	sum        hash.Hash
	fail       func(error) error
	done       bool
}

func (v *verified) Read(p []byte) (int, error) {
	if v.done {
		return 0, io.EOF
	}
	if left := v.size - v.read; int64(len(p)) > left {
		p = p[:left]
	}
	n := 0
	var err error
	if len(p) > 0 {
		n, err = v.ReadCloser.Read(p)
		v.sum.Write(p[:n])
		v.read += int64(n)
	}
	if v.read < v.size {
		switch {
		case err == io.EOF:
			return 0, v.fail(fmt.Errorf("%w: blob sha256:%s is short: %d of %d bytes", ErrMalformed, v.want, v.read, v.size))
		case err != nil:
			return n, v.fail(fmt.Errorf("reading blob sha256:%s: %w", v.want, err))
		}
		return n, nil
	}
	// The declared length is reached: check before handing these bytes over.
	var one [1]byte
	if extra, perr := io.ReadFull(v.ReadCloser, one[:]); extra > 0 {
		return 0, v.fail(fmt.Errorf("%w: blob sha256:%s is longer than its %d bytes", ErrMalformed, v.want, v.size))
	} else if perr != io.EOF {
		return 0, v.fail(fmt.Errorf("reading blob sha256:%s: %w", v.want, perr))
	}
	if got := hex.EncodeToString(v.sum.Sum(nil)); got != v.want {
		return 0, v.fail(fmt.Errorf("%w: blob sha256:%s has digest sha256:%s", ErrMalformed, v.want, got))
	}
	v.done = true
	return n, nil
}

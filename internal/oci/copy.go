package oci

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// confirm asks repository for the image with digest and returns the
// registry's answer: its digest, media type and size. An image the registry
// does not have is ErrNotFound.
func confirm(ctx context.Context, repository Repository, digest string) (Image, error) {
	repo, err := repositoryOf(repository.Name)
	if err != nil {
		return Image{}, err
	}
	ref, err := name.NewDigest(repo.Name()+"@"+digest, name.StrictValidation)
	if err != nil {
		return Image{}, fmt.Errorf("%w: digest %q: %w", errMalformed, digest, err)
	}
	d, err := remote.Head(ref, options(ctx, repository.Credential)...)
	if err != nil {
		return Image{}, classify("confirming "+ref.String(), err, errMalformed)
	}
	return Image{Repository: repo.Name(), Digest: d.Digest.String(), MediaType: string(d.MediaType), Size: d.Size}, nil
}

// confirmAs confirms an image written to repository and requires the
// registry's answer to be exactly what was written. Any other answer means
// the image is not published.
func confirmAs(ctx context.Context, repository Repository, written v1.Descriptor) (Image, error) {
	got, err := confirm(ctx, repository, written.Digest.String())
	if err != nil {
		return Image{}, err
	}
	if got.Digest != written.Digest.String() || got.MediaType != string(written.MediaType) || got.Size != written.Size {
		return Image{}, fmt.Errorf("%w: asked for %s, the registry answered %s (%s, %d bytes), not %s (%s, %d bytes)",
			errMalformed, written.Digest, got.Digest, got.MediaType, got.Size, written.Digest, written.MediaType, written.Size)
	}
	return got, nil
}

// Copy copies from's image for platform into repository to, tagged tag,
// and returns it as the registry answers for its digest. The digest is
// kept: the manifest is written as the source serves it. A source that is
// an index is resolved to its one image for platform; a source that is an
// image must be for platform.
func Copy(ctx context.Context, from Source, to Repository, tag, platform string) (Image, error) {
	// A source is an image as a project names it (redis:7, example/cache:7),
	// so Docker's defaults apply: docker.io and its library namespace. A
	// destination is AppTown's own repository, named in full.
	src, err := name.ParseReference(from.Reference, name.WeakValidation)
	if err != nil {
		return Image{}, fmt.Errorf("%w: reference %q: %w", errMalformed, from.Reference, err)
	}
	repo, err := repositoryOf(to.Name)
	if err != nil {
		return Image{}, err
	}
	dest, err := name.NewTag(repo.Name()+":"+tag, name.StrictValidation)
	if err != nil {
		return Image{}, fmt.Errorf("%w: tag %q: %w", errMalformed, tag, err)
	}
	read := options(ctx, from.Credential)
	d, err := remote.Get(src, read...)
	if err != nil {
		return Image{}, classify("reading "+src.String(), err, errMalformed)
	}
	desc := d.Descriptor
	if d.MediaType.IsIndex() {
		if desc, err = forPlatform(d, src.String(), platform); err != nil {
			return Image{}, err
		}
	} else if !d.MediaType.IsImage() || d.MediaType.IsSchema1() {
		return Image{}, fmt.Errorf("%w: %s is a %q, not an image", errUnsupported, src, d.MediaType)
	}
	img, err := remote.Image(src.Context().Digest(desc.Digest.String()), read...)
	if err != nil {
		return Image{}, classify("reading "+src.String(), err, errMalformed)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return Image{}, classify("reading "+src.String()+"'s config", err, errMalformed)
	}
	got, err := platformOf(cfg.OS, cfg.Architecture, cfg.Variant)
	if err != nil {
		return Image{}, err
	}
	if got != platform {
		return Image{}, fmt.Errorf("%w: %s is for %s, not %s", errUnsupported, src, got, platform)
	}
	if err := remote.Write(dest, img, options(ctx, to.Credential)...); err != nil {
		return Image{}, classify("copying to "+dest.String(), err, errMalformed)
	}
	return confirmAs(ctx, to, desc)
}

// Tag points tag in repository at the image with digest, which the
// registry already holds: its manifest is put again under the tag, and no
// image is written. A tag is for people and retention rules; the digest is
// the record. An image the registry does not have is ErrNotFound.
func Tag(ctx context.Context, repository Repository, digest, tag string) error {
	repo, err := repositoryOf(repository.Name)
	if err != nil {
		return err
	}
	ref, err := name.NewDigest(repo.Name()+"@"+digest, name.StrictValidation)
	if err != nil {
		return fmt.Errorf("%w: digest %q: %w", errMalformed, digest, err)
	}
	dest, err := name.NewTag(repo.Name()+":"+tag, name.StrictValidation)
	if err != nil {
		return fmt.Errorf("%w: tag %q: %w", errMalformed, tag, err)
	}
	opts := options(ctx, repository.Credential)
	d, err := remote.Get(ref, opts...)
	if err != nil {
		return classify("reading "+ref.String(), err, errMalformed)
	}
	return classify("tagging "+dest.String(), remote.Tag(dest, d, opts...), nil)
}

// forPlatform is an index's first image for platform. Attestation manifests
// name no platform of their own and never match.
func forPlatform(d *remote.Descriptor, ref, platform string) (v1.Descriptor, error) {
	index, err := d.ImageIndex()
	if err != nil {
		return v1.Descriptor{}, classify("reading "+ref, err, errMalformed)
	}
	m, err := index.IndexManifest()
	if err != nil {
		return v1.Descriptor{}, classify("reading "+ref, err, errMalformed)
	}
	for _, c := range m.Manifests {
		if c.Platform == nil || !c.MediaType.IsImage() || c.MediaType.IsSchema1() {
			continue
		}
		if p, err := platformOf(c.Platform.OS, c.Platform.Architecture, c.Platform.Variant); err == nil && p == platform {
			return c, nil
		}
	}
	return v1.Descriptor{}, fmt.Errorf("%w: %s has no image for %s", errUnsupported, ref, platform)
}

// Delete removes the image digest names from repository: the manifest,
// whose blobs the registry then collects. An image already gone is not an
// error.
func Delete(ctx context.Context, repository Repository, digest string) error {
	repo, err := repositoryOf(repository.Name)
	if err != nil {
		return err
	}
	err = remote.Delete(repo.Digest(digest), options(ctx, repository.Credential)...)
	if err = classify("deleting "+repository.Name+"@"+digest, err, ErrUnavailable); errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

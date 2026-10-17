// Package oci publishes, copies, tags and confirms container images in OCI
// registries, by digest. It wraps go-containerregistry and knows nothing of
// the forge.
//
// An image is published only after the registry, asked by digest, answers
// with the digest, media type and size that were written: the registry's
// answer is the record, never the word of whoever produced the image.
// Every image here is one image manifest for one platform. An index, an
// attestation manifest or a second manifest is refused by name.
//
// Failures are classified, and the classes stay distinct:
//   - ErrUnavailable: the registry could not be reached or answered 429/5xx;
//   - ErrUnauthorised: the registry refused the credential (401, 403);
//   - ErrNotFound: the registry has no such image or repository (404);
//   - ErrMalformed: the image does not match its own declarations (a blob
//     short, long or with another digest; unreadable JSON), or a registry's
//     answer cannot be used (a read-back naming another digest, media type
//     or size);
//   - ErrUnsupported: a well-formed thing this package does not publish (an
//     index, attestations, another platform, a digest algorithm other than
//     sha256).
//
// A cancelled context is returned as itself. A layout that cannot be read
// fails with its own error, never taken for an unreachable registry. Any
// other failure (a registry status outside these) is returned unclassified:
// still an error, never success and never one of the above.
package oci

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

var (
	ErrUnavailable  = errors.New("registry unavailable")
	ErrUnauthorised = errors.New("registry refused the credential")
	ErrNotFound     = errors.New("not in the registry")
	ErrMalformed    = errors.New("malformed image")
	ErrUnsupported  = errors.New("unsupported image")
)

// Credential is a registry login, held in memory for one operation. Empty
// is anonymous.
type Credential struct{ Username, Password string }

// Repository is a registry-qualified repository and the credential that
// writes or reads it.
type Repository struct {
	Name       string // e.g. registry.example.com/team/app
	Credential Credential
}

// Source is an image to copy: a reference by tag or digest, and the
// credential that reads it.
type Source struct {
	Reference  string
	Credential Credential
}

// Image is an image as a registry confirmed it.
type Image struct {
	Repository string // registry-qualified
	Digest     string // sha256:<hex>
	MediaType  string
	Size       int64
}

// Reference is the image by digest: <repository>@sha256:<hex>.
func (i Image) Reference() string { return i.Repository + "@" + i.Digest }

// Platform is the canonical OCI os/arch for a machine's raw facts:
// x86_64 is amd64 and aarch64 is arm64. Anything else is refused, never
// guessed.
func Platform(osName, arch string) (string, error) {
	osName = strings.ToLower(strings.TrimSpace(osName))
	if osName != "linux" {
		return "", fmt.Errorf("%w: operating system %q", ErrUnsupported, osName)
	}
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "x86_64", "amd64":
		return "linux/amd64", nil
	case "aarch64", "arm64":
		return "linux/arm64", nil
	}
	return "", fmt.Errorf("%w: architecture %q", ErrUnsupported, arch)
}

// platformOf is an image config's platform in canonical form. A variant is
// accepted only where it names the architecture's baseline (arm64/v8).
func platformOf(osName, arch, variant string) (string, error) {
	p, err := Platform(osName, arch)
	if err != nil {
		return "", err
	}
	if variant != "" && !(p == "linux/arm64" && variant == "v8") {
		return "", fmt.Errorf("%w: platform %s/%s", ErrUnsupported, p, variant)
	}
	return p, nil
}

func options(ctx context.Context, c Credential) []remote.Option {
	auth := authn.Anonymous
	if c.Username != "" || c.Password != "" {
		auth = &authn.Basic{Username: c.Username, Password: c.Password}
	}
	return []remote.Option{remote.WithContext(ctx), remote.WithAuth(auth)}
}

func repositoryOf(s string) (name.Repository, error) {
	r, err := name.NewRepository(s)
	if err != nil {
		return name.Repository{}, fmt.Errorf("%w: repository %q: %w", ErrMalformed, s, err)
	}
	return r, nil
}

// RefusedBy is the host that refused a request with 401 or 403, from the
// response itself: a copy reads its source lazily while it writes, so which
// step failed does not say which registry refused. ok is false when err is
// no such refusal or the response does not say.
func RefusedBy(err error) (host string, ok bool) {
	var te *transport.Error
	if !errors.As(err, &te) || te.Request == nil || te.Request.URL == nil {
		return "", false
	}
	if te.StatusCode != http.StatusUnauthorized && te.StatusCode != http.StatusForbidden {
		return "", false
	}
	return te.Request.URL.Host, true
}

// classify names a registry failure's class. Failures this package already
// classified, and a cancelled context, are returned as they are. A failure
// that is neither the registry's HTTP answer nor the network is otherwise,
// where that is known, the registry's answer being unusable.
func classify(what string, err error, otherwise error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrUnavailable, ErrUnauthorised, ErrNotFound, ErrMalformed, ErrUnsupported, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return err
		}
	}
	var te *transport.Error
	if errors.As(err, &te) {
		switch {
		case te.StatusCode == http.StatusUnauthorized || te.StatusCode == http.StatusForbidden:
			// The registry's answer is kept, so RefusedBy can name who refused.
			return fmt.Errorf("%s: %w: %w", what, ErrUnauthorised, err)
		case te.StatusCode == http.StatusNotFound:
			return fmt.Errorf("%s: %w: %w", what, ErrNotFound, err)
		case te.StatusCode == http.StatusTooManyRequests || te.StatusCode >= 500:
			return fmt.Errorf("%s: %w: %w", what, ErrUnavailable, err)
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	var ne net.Error
	var ue *url.Error
	if errors.As(err, &ne) || errors.As(err, &ue) {
		return fmt.Errorf("%s: %w: %w", what, ErrUnavailable, err)
	}
	if otherwise != nil {
		return fmt.Errorf("%s: %w: %w", what, otherwise, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

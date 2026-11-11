package oci

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// imageLeft is an image the registry refused to delete, with its answer.
type imageLeft struct{ Reference, Why string }

// Trim deletes only expired recorded digests the repository confirms holding.
// All presence reads finish before deletion. A failed read deletes nothing.
// Callers supply references from any source repository: copies keep the digest.
func Trim(ctx context.Context, repo Repository, expired, keep []string) ([]string, []imageLeft, error) {
	protected := map[string]bool{}
	for _, ref := range keep {
		protected[imageDigest(ref)] = true
	}
	candidates := []string{}
	for _, ref := range expired {
		digest := imageDigest(ref)
		if digest == "" {
			return nil, nil, fmt.Errorf("retention requires image digests: %s", ref)
		}
		if protected[digest] || slices.Contains(candidates, digest) {
			continue
		}
		if _, err := confirm(ctx, repo, digest); errors.Is(err, errNotFound) {
			continue
		} else if err != nil {
			return nil, nil, err
		}
		candidates = append(candidates, digest)
	}
	var deleted []string
	var left []imageLeft
	for _, digest := range candidates {
		ref := repo.Name + "@" + digest
		if err := Delete(ctx, repo, digest); err != nil {
			why := err.Error()
			if deleteUnsupported(err) {
				why = "this registry does not delete images; its space is kept"
			}
			left = append(left, imageLeft{ref, why})
		} else {
			deleted = append(deleted, ref)
		}
	}
	return deleted, left, nil
}

func imageDigest(ref string) string {
	if strings.HasPrefix(ref, "sha256:") {
		return ref
	}
	_, digest, _ := strings.Cut(ref, "@")
	return digest
}

// deleteUnsupported distinguishes registries that cannot delete manifests.
func deleteUnsupported(err error) bool {
	var response *transport.Error
	if !errors.As(err, &response) {
		return false
	}
	if response.StatusCode == http.StatusMethodNotAllowed {
		return true
	}
	for _, detail := range response.Errors {
		if string(detail.Code) == "UNSUPPORTED" {
			return true
		}
	}
	return false
}

// Present checks every recorded digest in a copy's repository before any source
// is read. A failed presence check is an error, never missing.
func Present(ctx context.Context, repo Repository, refs []string) (bool, error) {
	for _, ref := range refs {
		if _, err := confirm(ctx, repo, imageDigest(ref)); errors.Is(err, errNotFound) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	return true, nil
}

// CheckManifest asks the named registry for the immutable manifest. It does
// not turn an unreadable config or layer into a missing manifest.
func CheckManifest(ctx context.Context, source Source) error {
	ref, err := name.NewDigest(source.Reference, name.StrictValidation)
	if err != nil {
		return err
	}
	_, err = remote.Head(ref, options(ctx, source.Credential)...)
	return classify("checking "+source.Reference, err, errMalformed)
}

// ManifestMissing is the registry's explicit MANIFEST_UNKNOWN response. Other
// 404s (a proxy path, a denied private repository) do not authorize rebuilding.
func ManifestMissing(err error) bool {
	var response *transport.Error
	if !errors.As(err, &response) || response.StatusCode != http.StatusNotFound {
		return false
	}
	for _, detail := range response.Errors {
		if string(detail.Code) == "MANIFEST_UNKNOWN" {
			return true
		}
	}
	// HEAD responses have no body; the registry's digest-specific manifest
	// endpoint answering 404 is the same absence. Other paths remain errors.
	return response.Request != nil && strings.Contains(response.Request.URL.Path, "/manifests/sha256:")
}

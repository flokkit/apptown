package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Observation describes one immutable platform image without copying, pulling
// layers or publishing it. Config excludes only creation/history/rootfs facts;
// callers compare the remaining runtime configuration without dropping fields.
type Observation struct {
	Repository   string
	Manifest     string
	ConfigDigest string
	Platform     string
	Config       json.RawMessage
}

const maxObservationMetadata = 2 << 20

type observationTransport struct{ base http.RoundTripper }

func (t observationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.LimitReader(response.Body, maxObservationMetadata+1), response.Body}
	return response, nil
}

// Observe requires a digest and refuses indexes. A tag or publisher version
// label is never a runtime version observation.
func Observe(ctx context.Context, source Source) (Observation, error) {
	var out Observation
	ref, err := name.NewDigest(source.Reference, name.StrictValidation)
	if err != nil {
		return out, fmt.Errorf("%w: image digest: %w", errMalformed, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	opts := append(options(ctx, source.Credential), remote.WithTransport(observationTransport{registryTransport}))
	d, err := remote.Get(ref, opts...)
	if err != nil {
		return out, classify("observing image manifest", err, errMalformed)
	}
	if !d.MediaType.IsImage() || d.MediaType.IsSchema1() {
		return out, fmt.Errorf("%w: observation requires one platform image", errUnsupported)
	}
	if len(d.Manifest) > maxObservationMetadata || observationDigest(d.Manifest) != ref.DigestStr() {
		return out, fmt.Errorf("%w: image manifest identity or size", errMalformed)
	}
	image, err := d.Image()
	if err != nil {
		return out, classify("observing image", err, errMalformed)
	}
	manifest, err := image.Manifest()
	if err != nil {
		return out, classify("observing image manifest", err, errMalformed)
	}
	raw, err := image.RawConfigFile()
	if err != nil {
		return out, classify("observing image config", err, errMalformed)
	}
	if len(raw) > maxObservationMetadata || observationDigest(raw) != manifest.Config.Digest.String() || int64(len(raw)) != manifest.Config.Size {
		return out, fmt.Errorf("%w: image config identity or size", errMalformed)
	}
	cfg, err := image.ConfigFile()
	if err != nil {
		return out, classify("reading image platform", err, errMalformed)
	}
	platform, err := platformOf(cfg.OS, cfg.Architecture, cfg.Variant)
	if err != nil {
		return out, err
	}
	var all map[string]json.RawMessage
	if err = json.Unmarshal(raw, &all); err != nil || all == nil {
		return out, fmt.Errorf("%w: image config object", errMalformed)
	}
	for _, key := range []string{"created", "history", "rootfs"} {
		delete(all, key)
	}
	normalized, err := json.Marshal(all)
	if err != nil {
		return out, fmt.Errorf("%w: image config: %w", errMalformed, err)
	}
	return Observation{Repository: ref.Context().Name(), Manifest: ref.DigestStr(), ConfigDigest: manifest.Config.Digest.String(), Platform: platform, Config: normalized}, nil
}
func observationDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

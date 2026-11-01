package instanceupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const publisher = "https://github.com/flokkit/apptown"
const issuer = "https://token.actions.githubusercontent.com"

// Evidence retains what was authenticated, separate from installed provenance.
type Evidence struct {
	ManifestSHA256  string `json:"manifestSHA256"`
	ChecksumsSHA256 string `json:"checksumsSHA256"`
	Signer          string `json:"signer"`
	Issuer          string `json:"issuer"`
	SourceCommit    string `json:"sourceCommit"`
}

type signedRelease struct {
	Manifest                                Manifest
	Evidence                                Evidence
	ManifestBytes, Checksums, Bundle, Notes []byte
}

type releaseVerifier struct {
	trust func(context.Context) (root.TrustedMaterial, error)
}

func (v releaseVerifier) verify(ctx context.Context, tag string, manifest, checksums, signature, notes []byte) (signedRelease, error) {
	out := signedRelease{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	m, err := decodeManifest(manifest, tag)
	if err != nil {
		return out, err
	}
	entries, err := checksumEntries(checksums)
	if err != nil {
		return out, err
	}
	if entries[manifestName] != hashBytes(manifest) || entries[notesName] != hashBytes(notes) || m.Notes.SHA256 != hashBytes(notes) || m.Notes.Size != int64(len(notes)) {
		return out, fmt.Errorf("release manifest or notes digest does not match")
	}
	for _, archive := range m.Archives {
		if entries[archive.Name] != archive.SHA256 {
			return out, fmt.Errorf("release archive digest does not match")
		}
	}
	var b bundle.Bundle
	if err := json.Unmarshal(signature, &b); err != nil {
		return out, fmt.Errorf("invalid signature bundle: %w", err)
	}
	material, err := v.trust(ctx)
	if err != nil {
		return out, fmt.Errorf("release trust unavailable: %w", err)
	}
	verifier, err := verify.NewVerifier(material, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return out, fmt.Errorf("release verifier unavailable: %w", err)
	}
	signer := publisher + "/.github/workflows/images.yml@refs/tags/" + tag
	identity, err := verify.NewShortCertificateIdentity(issuer, "", signer, "")
	if err != nil {
		return out, err
	}
	identity.SourceRepositoryURI = publisher
	identity.SourceRepositoryDigest = m.Commit
	identity.SourceRepositoryRef = "refs/tags/" + tag
	digest := sha256.Sum256(checksums)
	_, err = verifier.Verify(&b, verify.NewPolicy(verify.WithArtifactDigest("sha256", digest[:]), verify.WithCertificateIdentity(identity)))
	if err != nil {
		return out, fmt.Errorf("release signature refused: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	out = signedRelease{Manifest: m, Evidence: Evidence{ManifestSHA256: hashBytes(manifest), ChecksumsSHA256: hashBytes(checksums), Signer: signer, Issuer: issuer, SourceCommit: m.Commit}, ManifestBytes: manifest, Checksums: checksums, Bundle: signature, Notes: notes}
	return out, nil
}

func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func checksumEntries(raw []byte) (map[string]string, error) {
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') || !hexOf(line[:64], 64) {
			return nil, fmt.Errorf("invalid release checksum entry")
		}
		name := line[66:]
		if !validMember(name) || strings.Contains(name, "/") || out[name] != "" {
			return nil, fmt.Errorf("invalid or duplicate release checksum name")
		}
		out[name] = line[:64]
	}
	if len(out) != 6 {
		return nil, fmt.Errorf("release checksums lack exact manifest, notes and four archives")
	}
	return out, nil
}

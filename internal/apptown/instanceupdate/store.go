package instanceupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/flokkit/apptown/internal/durable"
)

type state struct {
	Format      int                  `json:"format"`
	Channel     string               `json:"channel"`
	LastAttempt *time.Time           `json:"lastAttempt,omitempty"`
	LastSuccess *time.Time           `json:"lastSuccess,omitempty"`
	Candidate   *Candidate           `json:"candidate,omitempty"`
	FailureCode string               `json:"failureCode,omitempty"`
	Failure     string               `json:"failure,omitempty"`
	Pages       map[string]pageCache `json:"pages,omitempty"`
}

type evidencePacket struct {
	Format       int    `json:"format"`
	Tag          string `json:"tag"`
	Manifest     []byte `json:"manifest"`
	Checksums    []byte `json:"checksums"`
	Bundle       []byte `json:"bundle"`
	BundleSHA256 string `json:"bundleSHA256"`
	Notes        []byte `json:"notes"`
}

type cacheStore struct{ dir string }

func (s cacheStore) read(channel string) (state, error) {
	out := state{Format: 1, Channel: channel}
	raw, err := readBounded(filepath.Join(s.dir, "state.json"), 32<<20)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("reading release status: %w", err)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return state{}, fmt.Errorf("invalid release status: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return state{}, fmt.Errorf("invalid trailing release status")
	}
	if out.Format != 1 || (out.Channel != "stable" && out.Channel != "prerelease") || (out.LastSuccess != nil && out.LastAttempt == nil) || len(out.Pages) > 5 {
		return state{}, fmt.Errorf("invalid release status record")
	}
	if out.Candidate != nil {
		c := out.Candidate
		if !validTag(c.Version) || !hexOf(c.Commit, 40) || !hexOf(c.Evidence.ManifestSHA256, 64) || c.Evidence.SourceCommit != c.Commit {
			return state{}, fmt.Errorf("invalid cached release identity")
		}
	}
	if out.Candidate != nil {
		if err := s.checkEvidence(out.Candidate); err != nil {
			return state{}, err
		}
	}
	if out.Channel != channel {
		return state{Format: 1, Channel: channel, Pages: out.Pages}, nil
	}
	return out, nil
}

func (s cacheStore) save(st state) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if len(raw) > 32<<20 {
		return fmt.Errorf("release status exceeds cache limit")
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	_, err = durable.Write(s.dir, "state.json", raw, 0600, false)
	return err
}

func (s cacheStore) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return durable.SyncDir(filepath.Dir(s.dir))
}

func (s cacheStore) retain(r signedRelease) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	name := r.Manifest.Version + ".json"
	packet := evidencePacket{Format: 1, Tag: r.Manifest.Version, Manifest: r.ManifestBytes, Checksums: r.Checksums, Bundle: r.Bundle, BundleSHA256: hashBytes(r.Bundle), Notes: r.Notes}
	raw, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	existed, err := durable.Write(s.dir, name, raw, 0600, true)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}
	previous, err := readBounded(filepath.Join(s.dir, name), 16<<20)
	if err != nil {
		return err
	}
	var held evidencePacket
	if err := json.Unmarshal(previous, &held); err != nil {
		return fmt.Errorf("immutable release evidence is unreadable: %w", err)
	}
	// A refreshed signature bundle may differ. The already authenticated content
	// cannot. Keep the original proof rather than replace it with a new assertion.
	if held.Format != 1 || held.Tag != packet.Tag || held.BundleSHA256 != hashBytes(held.Bundle) || len(held.Bundle) == 0 || !bytes.Equal(held.Manifest, packet.Manifest) || !bytes.Equal(held.Checksums, packet.Checksums) || !bytes.Equal(held.Notes, packet.Notes) {
		return fmt.Errorf("immutable release identity changed; original evidence retained")
	}
	return durable.SyncDir(s.dir)
}

func readBounded(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("release cache exceeds size limit")
	}
	return raw, nil
}

// Cache observations are trusted owner records, but their retained bytes still
// need to exist and agree. A missing or corrupted witness is never availability.
func (s cacheStore) checkEvidence(c *Candidate) error {
	raw, err := readBounded(filepath.Join(s.dir, c.Version+".json"), 16<<20)
	if err != nil {
		return fmt.Errorf("retained release evidence unavailable: %w", err)
	}
	var p evidencePacket
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("retained release evidence unreadable: %w", err)
	}
	if p.Format != 1 || p.Tag != c.Version || p.BundleSHA256 != hashBytes(p.Bundle) || len(p.Bundle) == 0 || hashBytes(p.Manifest) != c.Evidence.ManifestSHA256 || hashBytes(p.Checksums) != c.Evidence.ChecksumsSHA256 || string(p.Notes) != c.Notes || c.Evidence.Signer != publisher+"/.github/workflows/images.yml@refs/tags/"+c.Version || c.Evidence.Issuer != issuer {
		return fmt.Errorf("retained release evidence changed")
	}
	m, err := decodeManifest(p.Manifest, p.Tag)
	if err != nil {
		return err
	}
	a, ok := m.Archives[c.Platform]
	if !ok || a.Artifact != c.Archive || m.Commit != c.Commit || m.Notes.SHA256 != hashBytes(p.Notes) {
		return fmt.Errorf("cached release projection disagrees with evidence")
	}
	return nil
}

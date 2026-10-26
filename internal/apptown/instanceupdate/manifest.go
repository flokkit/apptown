// Package instanceupdate owns authenticated release availability. It does not
// install releases or claim that a running installation is update-ready.
package instanceupdate

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	ManifestName  = "UPDATE-MANIFEST.json"
	NotesName     = "RELEASE-NOTES.md"
	ChecksumsName = "SHA256SUMS"
	BundleName    = "SHA256SUMS.sigstore.json"
)

// Manifest binds every artifact to one authenticated release. Zero updater
// protocol and unqualified worker state explicitly decline replacement support.
type Manifest struct {
	Format        int                `json:"format"`
	Version       string             `json:"version"`
	Commit        string             `json:"commit"`
	Notes         Artifact           `json:"notes"`
	Archives      map[string]Archive `json:"archives"`
	Images        map[string]Image   `json:"images"`
	Compatibility Compatibility      `json:"compatibility"`
}

type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Archive struct {
	Artifact
	// Members names paths relative to the archive's single release directory.
	Members map[string]string `json:"members"`
}

type Image struct {
	Repository string            `json:"repository"`
	Index      string            `json:"index"`
	Platforms  map[string]string `json:"platforms"`
}

type Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type Compatibility struct {
	Schema               Range  `json:"sourceSchema"`
	Layout               Range  `json:"sourceLayout"`
	SchemaResult         int    `json:"schemaResult"`
	LayoutResult         int    `json:"layoutResult"`
	CLIProtocol          int    `json:"cliProtocol"`
	DaemonWorkerProtocol int    `json:"daemonWorkerProtocol"`
	WorkerProtocol       Range  `json:"workerProtocol"`
	UpdaterProtocol      int    `json:"updaterProtocol"`
	WorkerStateQualified bool   `json:"workerStateQualified"`
	AgentABIQualified    bool   `json:"agentABIQualified"`
	DatabaseTransition   string `json:"databaseTransition"`
	RuntimeTransition    string `json:"runtimeTransition"`
}

func decodeManifest(raw []byte, tag string) (Manifest, error) {
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid update manifest: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, fmt.Errorf("update manifest contains trailing JSON")
	}
	if err := m.validate(tag); err != nil {
		return m, err
	}
	return m, nil
}

func (m Manifest) validate(tag string) error {
	if m.Format != 1 || !validTag(tag) || m.Version != tag || !hexOf(m.Commit, 40) {
		return fmt.Errorf("unsupported update manifest identity")
	}
	if m.Notes.Name != NotesName || !validArtifact(m.Notes) {
		return fmt.Errorf("invalid release notes identity")
	}
	if len(m.Archives) != 4 || len(m.Images) != 3 {
		return fmt.Errorf("release artifact inventory is incomplete")
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
		a, ok := m.Archives[platform]
		name := "apptown-" + strings.TrimPrefix(tag, "v") + "-" + strings.ReplaceAll(platform, "/", "-") + ".tar.gz"
		if !ok || a.Name != name || !validArtifact(a.Artifact) {
			return fmt.Errorf("invalid archive for %s", platform)
		}
		for _, required := range []string{"bin/apptownd", "bin/apptown", "bin/apptown-worker", "bin/apptown-linux", "deploy/apptown-agent/Dockerfile", "scripts/apptown-db-backup.sh"} {
			if !hexOf(a.Members[required], 64) {
				return fmt.Errorf("archive %s lacks paired asset %s", platform, required)
			}
		}
		stock, units := false, false
		for member, digest := range a.Members {
			if !validMember(member) || !hexOf(digest, 64) {
				return fmt.Errorf("archive %s has invalid member identity", platform)
			}
			stock = stock || strings.HasPrefix(member, "share/apptown/updates/")
			units = units || strings.HasPrefix(member, "deploy/systemd/")
		}
		if !stock || !units {
			return fmt.Errorf("archive %s lacks stock or service assets", platform)
		}
	}
	for _, name := range []string{"apptown", "apptown-worker", "apptown-agent"} {
		image, ok := m.Images[name]
		if !ok || image.Repository != "ghcr.io/flokkit/"+name || !validDigest(image.Index) || len(image.Platforms) != 2 {
			return fmt.Errorf("invalid image identity for %s", name)
		}
		for _, platform := range []string{"linux/amd64", "linux/arm64"} {
			if !validDigest(image.Platforms[platform]) {
				return fmt.Errorf("image %s lacks platform %s", name, platform)
			}
		}
	}
	c := m.Compatibility
	if !validRange(c.Schema) || !validRange(c.Layout) || c.SchemaResult < c.Schema.Max || c.LayoutResult < c.Layout.Max || c.CLIProtocol < 1 || !validRange(c.WorkerProtocol) || c.DaemonWorkerProtocol < c.WorkerProtocol.Min || c.DaemonWorkerProtocol > c.WorkerProtocol.Max || c.UpdaterProtocol < 0 {
		return fmt.Errorf("invalid release compatibility declaration")
	}
	if c.DatabaseTransition != "same-major" || c.RuntimeTransition != "unchanged" {
		return fmt.Errorf("unsupported engine transition declaration")
	}
	return nil
}

func validTag(tag string) bool {
	return strings.HasPrefix(tag, "v") && semver.IsValid(tag) && semver.Canonical(tag) == strings.TrimSuffix(tag, semver.Build(tag)) && !strings.ContainsAny(tag, "/\\")
}
func hexOf(s string, n int) bool {
	if len(s) != n || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func validDigest(s string) bool {
	return strings.HasPrefix(s, "sha256:") && hexOf(strings.TrimPrefix(s, "sha256:"), 64)
}
func validArtifact(a Artifact) bool {
	return validMember(a.Name) && !strings.Contains(a.Name, "/") && hexOf(a.SHA256, 64) && a.Size > 0
}
func validMember(s string) bool {
	return s != "" && s != "." && path.Clean(s) == s && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\\\x00\r\n")
}
func validRange(r Range) bool { return r.Min > 0 && r.Max >= r.Min }

package preview

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Preparation captures the exact routing owner's label before incarnation and
// task serialization. It does not recover labels from mutable project slugs.
func TestPreparedPreviewOriginsAreExactAndPartOfIncarnation(t *testing.T) {
	for _, domain := range []string{"preview.example", ""} {
		t.Run("domain="+domain, func(t *testing.T) {
			w := newWorld(t, "", "")
			w.env.Routes.PreviewScheme, w.env.Routes.PreviewDomain = "https", domain
			s := w.service(Dependencies{})
			ws := filepath.Join(t.TempDir(), "tree")
			writePreviewBootFixture(t, ws)
			c, err := docker.ReadWorkspaceContract(ws)
			if err != nil {
				t.Fatal(err)
			}
			for _, label := range []string{"change-17", "run-19", "original-release-3"} {
				p, err := s.preparePreview(t.Context(), w.projectID, "renamed", "owned-preview", label, ws, c, nil)
				if err != nil {
					t.Fatal(err)
				}
				want := ""
				if domain != "" {
					want = "https://" + label + ".preview.example"
				}
				if p.Origins != want || strings.Contains(p.Origins, "*") {
					t.Fatalf("label %q: %q want %q", label, p.Origins, want)
				}
				raw, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				var sent docker.Prepared
				if err := json.Unmarshal(raw, &sent); err != nil {
					t.Fatal(err)
				}
				if sent.Origins != want || sent.Public().Digest() != p.Public().Digest() {
					t.Fatal("serialization changed route identity")
				}
				old := p
				old.Origins = "https://*.preview.example"
				if old.Public().Digest() == p.Public().Digest() {
					t.Fatal("old wildcard incarnation reused")
				}
				// Retained requests are not rewritten by decoding with new code.
				before, err := json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(before, &sent); err != nil {
					t.Fatal(err)
				}
				after, err := json.Marshal(sent)
				if err != nil {
					t.Fatal(err)
				}
				if string(before) != string(after) || sent.Origins != "https://*.preview.example" {
					t.Fatal("retained request bytes changed")
				}
			}
		})
	}
}

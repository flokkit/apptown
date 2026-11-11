//go:build integration

package docker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

func TestIntegrationLandingBuildTagsLeaveOnlyKeptImages(t *testing.T) {
	dk := Scope{Client: CLI{Binary: "docker"}}
	// Isolate prefix-owned tags from every other local test and installation.
	prefix := fmt.Sprintf("retention-%d-", time.Now().UnixNano())
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "Dockerfile"), []byte("FROM scratch\nCOPY value /value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := appcontract.Parse([]byte("app:\n  web:\n    build: .\n    public: true\n    ports: [\"8080\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	var ids, tags []string
	for n := 0; n < 4; n++ {
		if err := os.WriteFile(filepath.Join(tree, "value"), []byte(fmt.Sprint(n)), 0600); err != nil {
			t.Fatal(err)
		}
		// BuildImages uses the same naming path as a no-worker landing. The
		// unique test prefix is supplied through the image's owned tag here.
		parts, _, err := NewHost(Dependencies{}).BuildImages(t.Context(), dk, fmt.Sprintf("build-retention-%d-%d", time.Now().UnixNano(), n), tree, c)
		if err != nil {
			t.Fatal(err)
		}
		tag := parts["web"]
		out, err := dk.Run(t.Context(), "image", "inspect", "--format", "{{.Id}}", tag)
		if err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(string(out))
		owned := prefix + fmt.Sprintf("preview-build-build-%d", n)
		if _, err := dk.Run(t.Context(), "tag", id, owned); err != nil {
			t.Fatal(err)
		}
		raw, err := dk.Run(t.Context(), "image", "inspect", "--format", "{{json .RepoTags}}", id)
		if err != nil {
			t.Fatal(err)
		}
		var original []string
		if err := json.Unmarshal(raw, &original); err != nil {
			t.Fatal(err)
		}
		for _, built := range original {
			if strings.HasPrefix(built, string(Current)+"preview-build-build-retention-") {
				if _, err := dk.Run(t.Context(), "rmi", built); err != nil {
					t.Fatal(err)
				}
			}
		}
		ids = append(ids, id)
		tags = append(tags, owned)
	}
	t.Cleanup(func() {
		for _, tag := range tags {
			_, _ = dk.Run(t.Context(), "rmi", tag)
		}
		for _, id := range ids {
			_, _ = dk.Run(t.Context(), "rmi", id)
		}
	})
	// The task's spoke policy keeps three, rather than the plan's older
	// running-plus-previous Docker exception. All images predate seven days.
	deleted, left, err := dk.TrimImages(t.Context(), ids[:1], ids[1:], prefix)
	if err != nil || len(left) != 0 || len(deleted) == 0 {
		t.Fatalf("trim: %v %v %v", deleted, left, err)
	}
	out, err := dk.Run(t.Context(), "image", "ls", "--format", "{{.Repository}}", prefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	remaining := strings.Fields(string(out))
	if len(remaining) != 3 {
		t.Fatalf("remaining build tags: %v", remaining)
	}
	for _, tag := range remaining {
		if !slices.Contains(tags[1:], tag) {
			t.Fatalf("expired build tag stayed: %s", tag)
		}
	}
}

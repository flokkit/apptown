package fly

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestFlyRetentionDeletesOnlyExpiredRecordedImagesAndReportsRefusal(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "unsupported"}[refuse], func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			var removed []string
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					removed = append(removed, r.URL.Path)
					if refuse {
						out.WriteHeader(405)
						return
					}
				}
				handler.ServeHTTP(out, r)
			}))
			defer server.Close()
			w.driver.options.registryHost = strings.TrimPrefix(server.URL, "http://")
			repo, err := w.driver.ImageStore(t.Context(), w.p)
			if err != nil {
				t.Fatal(err)
			}
			var refs []string
			for _, tag := range []string{"old", "kept", "human"} {
				image, err := random.Image(20, 1)
				if err != nil {
					t.Fatal(err)
				}
				ref, err := name.NewTag(repo + ":" + tag)
				if err != nil {
					t.Fatal(err)
				}
				if err := remote.Write(ref, image); err != nil {
					t.Fatal(err)
				}
				digest, err := image.Digest()
				if err != nil {
					t.Fatal(err)
				}
				refs = append(refs, "hub/app@"+digest.String())
			}
			deleted, left, err := w.driver.TrimImages(t.Context(), w.p, refs[:2], refs[1:2])
			if err != nil || len(removed) != 1 {
				t.Fatalf("trim: %v %v %v", deleted, left, err)
			}
			if refuse {
				if len(deleted) != 0 || len(left) != 1 || !strings.Contains(left[0].Why, "does not delete") {
					t.Fatalf("refused: %v %v", deleted, left)
				}
			} else if len(deleted) != 1 || len(left) != 0 {
				t.Fatalf("deleted: %v %v", deleted, left)
			}
			_, digest, _ := strings.Cut(refs[2], "@")
			ref, err := name.NewDigest(repo + "@" + digest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := remote.Head(ref); err != nil {
				t.Fatal("unrecorded image removed", err)
			}
		})
	}
}

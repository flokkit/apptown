package digitalocean

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/digitalocean/dofake"
)

func TestImageRetentionKeepsProtectedAndUnrecordedDOCRDigests(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "deletes", true: "unsupported"}[unsupported], func(t *testing.T) {
			t.Parallel()
			api := &dofake.API{}
			api.SeedRegistry("hub", "basic", 5, "shop")
			api.SeedManifests("shop", "sha256:old", "sha256:kept", "sha256:human")
			handler := api.Handler()
			var deletes []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					deletes = append(deletes, r.URL.Path)
					if unsupported {
						w.WriteHeader(405)
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			driver := NewDriver(Dependencies{Slug: "shop", Client: func(context.Context) (*Client, Config, error) { return NewClient("token", server.URL), Config{}, nil }}, Options{})
			deleted, left, err := driver.TrimImages(t.Context(), deploy.Placement{}, []string{"source/app@sha256:old", "source/app@sha256:kept"}, []string{"source/app@sha256:kept"})
			if err != nil || len(deletes) != 1 || !strings.HasSuffix(deletes[0], "sha256:old") {
				t.Fatalf("bounded deletes: %v %v", deletes, err)
			}
			if unsupported {
				if len(deleted) != 0 || len(left) != 1 || !strings.Contains(left[0].Why, "does not delete") {
					t.Fatalf("unsupported: %v %v", deleted, left)
				}
			} else if !slices.Equal(deleted, []string{"hub/shop@sha256:old"}) || len(left) != 0 {
				t.Fatalf("deleted: %v %v", deleted, left)
			}
		})
	}
}

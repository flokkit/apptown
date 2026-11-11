package portainer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

func TestImageRetentionUsesThePortainerProxyWithoutForcingDeletion(t *testing.T) {
	t.Parallel()
	oldID := "sha256:" + strings.Repeat("a", 64)
	keepID := "sha256:" + strings.Repeat("b", 64)
	oldRef := "registry.example/app@" + oldID
	keepRef := "registry.example/app@" + keepID
	var deletes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "fixture-key" {
			t.Error("proxy lost its endpoint credential")
			w.WriteHeader(403)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/endpoints/3/docker")
		if strings.HasSuffix(path, "/_ping") {
			w.Header().Set("API-Version", "1.47")
			fmt.Fprint(w, "OK")
			return
		}
		if strings.HasSuffix(path, "/version") {
			fmt.Fprint(w, `{"ApiVersion":"1.47","MinAPIVersion":"1.24","Os":"linux"}`)
			return
		}
		if r.Method == "DELETE" {
			if r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true" {
				t.Error("image deletion was forced")
			}
			deletes = append(deletes, path)
			_ = json.NewEncoder(w).Encode([]map[string]string{{"Untagged": oldRef}})
			return
		}
		image := func(id, ref string) map[string]any {
			return map[string]any{"Id": id, "RepoTags": []string{}, "RepoDigests": []string{ref}, "Size": 1}
		}
		switch {
		case strings.HasSuffix(path, "/images/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{image(oldID, oldRef), image(keepID, keepRef)})
		case strings.Contains(path, "/images/") && strings.HasSuffix(path, "/json"):
			if strings.Contains(path, strings.Repeat("a", 64)) {
				_ = json.NewEncoder(w).Encode(image(oldID, oldRef))
			} else {
				_ = json.NewEncoder(w).Encode(image(keepID, keepRef))
			}
		default:
			t.Errorf("unexpected proxy request %s %s", r.Method, path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	dir, err := os.MkdirTemp("/tmp", "image-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	proxy, err := StartDockerProxy(dir, deploymentEnvironment{ID: 3, Name: "lab"}, func(context.Context) (*Target, error) {
		return &Target{PortainerURL: server.URL, PortainerKey: "fixture-key"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	scope := docker.Scope{Client: docker.CLI{}, Host: proxy.Host()}
	driver := &Driver{ImageScope: func(context.Context) (docker.Scope, error) { return scope, nil }}
	deleted, left, err := driver.TrimImages(t.Context(), deploy.Placement{}, []string{oldRef, keepRef}, []string{keepRef})
	if err != nil || len(left) != 0 || len(deleted) != 1 || deleted[0] != oldRef || len(deletes) != 1 || !strings.Contains(deletes[0], oldID) {
		t.Fatalf("proxy trim: deleted=%v left=%v calls=%v err=%v", deleted, left, deletes, err)
	}
}

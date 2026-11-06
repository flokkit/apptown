package deployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/deploy/appcontract"
	"github.com/flokkit/apptown/internal/deploy/docker"
)

// Bundle receipts use the actual image IDs after the selected contexts
// build, read from the daemon the landing booted on.
func TestBundleImagesRecordsActualImages(t *testing.T) {
	t.Parallel()
	daemon := newDockerFake()
	daemon.SetReply(func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "image" && strings.Contains(strings.Join(args, " "), "{{.Id}}") {
			sum := sha256.Sum256([]byte(args[len(args)-1]))
			return []byte("sha256:" + hex.EncodeToString(sum[:])), nil, true
		}
		return nil, nil, false
	})
	contract, err := appcontract.Parse([]byte(`app:
  web:
    image: build:web
    ports: ["8080"]
    public: true
  worker:
    image: build:worker
init:
  - name: same-worker
    image: build:worker
  - name: migrate
    image: build:migrate
`))
	if err != nil {
		t.Fatal(err)
	}
	parts, init, err := docker.NewHost(docker.Dependencies{}).BuildImages(context.Background(), docker.Scope{Client: daemon}, "bundle-build", t.TempDir(), contract)
	if err != nil {
		t.Fatal(err)
	}
	if parts["web"] == parts["worker"] || parts["worker"] != init["same-worker"] || init["migrate"] == parts["web"] ||
		!strings.HasPrefix(parts["web"], "sha256:") || !strings.HasPrefix(init["migrate"], "sha256:") {
		t.Fatalf("bundle lost actual images: %v %v", parts, init)
	}
	for _, args := range daemon.Commands() {
		if len(args) > 0 && args[0] == "run" {
			t.Fatal("building images booted a container")
		}
	}
}

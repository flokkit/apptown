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
	plan := &appcontract.Plan{
		Parts: []appcontract.Part{{Name: "web", Image: "build:web"}, {Name: "worker", Image: "build:worker"}},
		Init:  []appcontract.InitContainer{{Name: "same-worker", Image: "build:worker"}, {Name: "migrate", Image: "build:migrate"}},
	}
	landed := LandingResult{Boot: LandingBoot{Boot: docker.BootResult{Stack: docker.ResolvedStack{Plan: plan}}}, Docker: docker.Scope{Client: daemon}}
	parts, init := landed.Images(context.Background())
	if parts["web"] == parts["worker"] || parts["worker"] != init["same-worker"] || init["migrate"] == parts["web"] ||
		!strings.HasPrefix(parts["web"], "sha256:") || !strings.HasPrefix(init["migrate"], "sha256:") {
		t.Fatalf("bundle lost actual images: %v %v", parts, init)
	}
}

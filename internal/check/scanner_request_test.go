package check

import (
	"slices"
	"testing"
)

// What a manifest says reaches its container: its image, its command, the
// network only when it needs it, and a cleared entrypoint when it says so.
func TestAScannersRequestCarriesItsManifest(t *testing.T) {
	t.Parallel()
	argv := []string{"sh", "-c", "exec \"$@\"", "sh", "trivy"}
	req := Scanner{Image: "aquasec/trivy:1", Network: true, NoEntrypoint: true}.Request("/tree", argv)
	if req.Image != "aquasec/trivy:1" || req.Network != "" || !req.NoEntrypoint || req.Workspace != "/tree" || !slices.Equal(req.Argv, argv) || req.Timeout != ScanTimeout {
		t.Fatalf("request %+v", req)
	}
	if offline := (Scanner{Image: "x"}).Request("/tree", nil); offline.Network != "none" || offline.NoEntrypoint {
		t.Fatalf("an offline scanner's request %+v", offline)
	}
}

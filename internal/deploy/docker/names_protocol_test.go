package docker

import (
	"testing"

	"flokkit.io/forge/internal/ids/idstest"
)

func TestDockerNamesArePinned(t *testing.T) {
	t.Parallel()
	pins := map[string]string{
		PreviewChangeName(idstest.N(7)):                                          "forge-preview-change-00000000-0007-7000-8000-000000000000",
		PreviewRunName(idstest.N(56)):                                            "forge-preview-run-00000000-0038-7000-8000-000000000000",
		PreviewLabelName("change-00000000-0007-7000-8000-000000000000"):          "forge-preview-change-00000000-0007-7000-8000-000000000000",
		PreviewLabelName("letterbox-release-4"):                                  "forge-preview-letterbox-release-4",
		ReleasePreviewLabel("letterbox", 4):                                      "letterbox-release-4",
		ContainerName("reading-list", "main"):                                    "forge-env-reading-list-main",
		ChangeStackID(idstest.N(7)):                                              "pvsnap-change-00000000-0007-7000-8000-000000000000",
		RunStepStackID(idstest.N(123)):                                           "pv00000000-007b-7000-8000-000000000000",
		ReleaseStackID("letterbox", 4):                                           "pvrel-letterbox-4",
		StackID("reading-list", "main"):                                          "env-reading-list-main",
		ServiceName("pv00000000-007b-7000-8000-000000000000", "postgres"):        "forge-svc-pv00000000-007b-7000-8000-000000000000-postgres",
		ServiceName("env-reading-list-main", "postgres"):                         "forge-svc-env-reading-list-main-postgres",
		NetworkName("pvsnap-change-00000000-0007-7000-8000-000000000000"):        "forge-net-pvsnap-change-00000000-0007-7000-8000-000000000000",
		NetworkName("env-reading-list-main"):                                     "forge-net-env-reading-list-main",
		InitName("pv00000000-007b-7000-8000-000000000000", "migrate"):            "forge-init-pv00000000-007b-7000-8000-000000000000-migrate",
		DataVolumeName("forge-preview-run-00000000-0038-7000-8000-000000000000"): "forge-data-forge-preview-run-00000000-0038-7000-8000-000000000000",
		DataVolumeName("forge-env-reading-list-main"):                            "forge-data-forge-env-reading-list-main",
		BuildTag("forge-preview-run-00000000-0038-7000-8000-000000000000"):       "forge-preview-build-forge-preview-run-00000000-0038-7000-8000-000000000000",
	}
	for got, want := range pins {
		if got != want {
			t.Errorf("name drifted: got %q, want %q", got, want)
		}
	}
}
func TestDockerNameInverses(t *testing.T) {
	t.Parallel()
	if id, ok := ParsePreviewChangeID("forge-preview-change-00000000-0061-7000-8000-000000000000"); !ok || id != idstest.N(97) {
		t.Fatalf("parsePreviewChangeID: %s %v", id, ok)
	}
	for _, bad := range []string{"forge-preview-change-", "forge-preview-change-7", "forge-preview-change-00000000-0007-7000-8000-000000000000x", "forge-preview-run-00000000-0007-7000-8000-000000000000",
		"forge-preview-change-00000000-0007-7000-8000-000000000000-old", "xforge-preview-change-00000000-0007-7000-8000-000000000000", "forge-preview-change-00000000-0000-0000-0000-000000000000"} {
		if _, ok := ParsePreviewChangeID(bad); ok {
			t.Errorf("parsePreviewChangeID accepted %q", bad)
		}
	}
	if s, ok := StackOfNetwork("forge-net-pvsnap-change-00000000-0007-7000-8000-000000000000"); !ok || s != "pvsnap-change-00000000-0007-7000-8000-000000000000" {
		t.Fatalf("stackOfNetwork: %q %v", s, ok)
	}
	if _, ok := StackOfNetwork("bridge"); ok {
		t.Error("bridge is not a forge network")
	}
	if c, ok := ContainerOfDataVolume("forge-data-forge-preview-run-00000000-0038-7000-8000-000000000000"); !ok || c != "forge-preview-run-00000000-0038-7000-8000-000000000000" {
		t.Fatalf("containerOfDataVolume: %q %v", c, ok)
	}
	if !IsLocalBuildTag("forge-preview-build-change-7") || IsLocalBuildTag("registry.example/app:1") {
		t.Error("isLocalBuildTag")
	}
}

func TestNameInverses(t *testing.T) {
	t.Parallel()
	if l, ok := previewLabel("forge-preview-letterbox-release-4"); !ok || l != "letterbox-release-4" {
		t.Fatalf("previewLabel: %q %v", l, ok)
	}
	if _, ok := previewLabel("forge-preview-"); ok {
		t.Error("an empty label is not a preview")
	}
	if !IsPreviewName("forge-preview-run-1") || IsPreviewName("forge-env-x-main") || IsPreviewName("forge-preview-build-x") == false {
		// forge-preview-build-* is an IMAGE tag, never a container name, so
		// the prefix test is fine as a container predicate.
		t.Error("IsPreviewName")
	}
	// The throwaway stacks, and only those: an environment's stack and an
	// agent step's stack are never "a preview's".
	for _, id := range []string{"pv00000000-007b-7000-8000-000000000000", "pvsnap-change-00000000-0007-7000-8000-000000000000", "pvrel-letterbox-4"} {
		if !isPreviewStackID(id) {
			t.Errorf("%q must be a preview stack", id)
		}
	}
	for _, id := range []string{"env-reading-list-main", "123", "pv", "pvx", "pv12a", "env-pv1-main"} {
		if isPreviewStackID(id) {
			t.Errorf("%q must not be a preview stack", id)
		}
	}
}

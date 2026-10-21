package docker

import (
	"testing"

	"flokkit.io/forge/internal/ids/idstest"
)

// The names on a host are a protocol: a deployment made before 1.0 keeps
// every name it was made with (Before1), byte for byte, and everything
// made now is named under Current.
func TestDockerNamesArePinned(t *testing.T) {
	t.Parallel()
	pins := map[string]string{
		PreviewChangeName(idstest.N(7)):                                                "apptown-preview-change-00000000-0007-7000-8000-000000000000",
		PreviewRunName(idstest.N(56)):                                                  "apptown-preview-run-00000000-0038-7000-8000-000000000000",
		PreviewLabelName("change-00000000-0007-7000-8000-000000000000"):                "apptown-preview-change-00000000-0007-7000-8000-000000000000",
		PreviewLabelName("letterbox-release-4"):                                        "apptown-preview-letterbox-release-4",
		ReleasePreviewLabel("letterbox", 4):                                            "letterbox-release-4",
		ChangeStackID(idstest.N(7)):                                                    "pvsnap-change-00000000-0007-7000-8000-000000000000",
		RunStepStackID(idstest.N(123)):                                                 "pv00000000-007b-7000-8000-000000000000",
		ReleaseStackID("letterbox", 4):                                                 "pvrel-letterbox-4",
		StackID("reading-list", "main"):                                                "env-reading-list-main",
		Current.Service("pv00000000-007b-7000-8000-000000000000", "postgres"):          "apptown-svc-pv00000000-007b-7000-8000-000000000000-postgres",
		Current.Network("pvsnap-change-00000000-0007-7000-8000-000000000000"):          "apptown-net-pvsnap-change-00000000-0007-7000-8000-000000000000",
		Current.Init("pv00000000-007b-7000-8000-000000000000", "migrate"):              "apptown-init-pv00000000-007b-7000-8000-000000000000-migrate",
		Current.DataVolume("apptown-preview-run-00000000-0038-7000-8000-000000000000"): "apptown-data-apptown-preview-run-00000000-0038-7000-8000-000000000000",
		Current.BuildTag("apptown-preview-run-00000000-0038-7000-8000-000000000000"):   "apptown-preview-build-apptown-preview-run-00000000-0038-7000-8000-000000000000",
		// A deployment made now.
		Current.Container("reading-list", "main"):               "apptown-env-reading-list-main",
		Current.Service("env-reading-list-main", "postgres"):    "apptown-svc-env-reading-list-main-postgres",
		Current.Network("env-reading-list-main"):                "apptown-net-env-reading-list-main",
		Current.NeedVolume("env-reading-list-main", "postgres"): "apptown-need-env-reading-list-main-postgres",
		Current.DataVolume("apptown-env-reading-list-main"):     "apptown-data-apptown-env-reading-list-main",
		// A deployment made before 1.0: today's live names, unchanged.
		Before1.Container("reading-list", "main"):               "forge-env-reading-list-main",
		Before1.Service("env-reading-list-main", "postgres"):    "forge-svc-env-reading-list-main-postgres",
		Before1.Network("env-reading-list-main"):                "forge-net-env-reading-list-main",
		Before1.Init("env-reading-list-main", "migrate"):        "forge-init-env-reading-list-main-migrate",
		Before1.NeedVolume("env-reading-list-main", "postgres"): "forge-need-env-reading-list-main-postgres",
		Before1.DataVolume("forge-env-reading-list-main"):       "forge-data-forge-env-reading-list-main",
		Before1.BuildTag("forge-env-reading-list-main-g12-web"): "forge-preview-build-forge-env-reading-list-main-g12-web",
	}
	for got, want := range pins {
		if got != want {
			t.Errorf("name drifted: got %q, want %q", got, want)
		}
	}
	for _, p := range Prefixes {
		if got, err := ParsePrefix(string(p)); err != nil || got != p {
			t.Errorf("ParsePrefix(%q) = %q, %v", p, got, err)
		}
	}
	for _, bad := range []string{"", "forge", "apptown", "other-"} {
		if _, err := ParsePrefix(bad); err == nil {
			t.Errorf("ParsePrefix accepted %q", bad)
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
		t.Error("bridge is not an AppTown network")
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

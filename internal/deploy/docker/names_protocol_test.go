package docker

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// keyed is a test id whose key (its last 12 hex digits) is n: idstest.N
// leaves the tail zero, so every one of its ids has the same key.
func keyed(n byte) ids.ID {
	id := idstest.N(uint64(n))
	id[15] = n
	return id
}

// The names on a host are a protocol: a deployment is named by its key
// under Current, byte for byte.
func TestDockerNamesArePinned(t *testing.T) {
	t.Parallel()
	id := idstest.Parse(t, "01a15e84-c8f1-7000-b757-3b89d1da8075")
	now := ByKey(id)
	pins := map[string]string{
		PreviewChangeName(idstest.N(7)):                                                "apptown-preview-change-00000000-0007-7000-8000-000000000000",
		PreviewRunName(idstest.N(56)):                                                  "apptown-preview-run-00000000-0038-7000-8000-000000000000",
		PreviewLabelName("change-00000000-0007-7000-8000-000000000000"):                "apptown-preview-change-00000000-0007-7000-8000-000000000000",
		PreviewLabelName("letterbox-release-4"):                                        "apptown-preview-letterbox-release-4",
		ReleasePreviewLabel("letterbox", 4):                                            "letterbox-release-4",
		ChangeStackID(idstest.N(7)):                                                    "pvsnap-change-00000000-0007-7000-8000-000000000000",
		RunStepStackID(idstest.N(123)):                                                 "pv00000000-007b-7000-8000-000000000000",
		ReleaseStackID("letterbox", 4):                                                 "pvrel-letterbox-4",
		Current.Service("pv00000000-007b-7000-8000-000000000000", "postgres"):          "apptown-svc-pv00000000-007b-7000-8000-000000000000-postgres",
		Current.Network("pvsnap-change-00000000-0007-7000-8000-000000000000"):          "apptown-net-pvsnap-change-00000000-0007-7000-8000-000000000000",
		Current.Init("pv00000000-007b-7000-8000-000000000000", "migrate"):              "apptown-init-pv00000000-007b-7000-8000-000000000000-migrate",
		Current.DataVolume("apptown-preview-run-00000000-0038-7000-8000-000000000000"): "apptown-data-apptown-preview-run-00000000-0038-7000-8000-000000000000",
		Current.BuildTag("apptown-preview-run-00000000-0038-7000-8000-000000000000"):   "apptown-preview-build-apptown-preview-run-00000000-0038-7000-8000-000000000000",
		// A deployment named by its key.
		now.Stack():                "dep-3b89d1da8075",
		now.App():                  "apptown-dep-3b89d1da8075",
		now.Service("postgres"):    "apptown-svc-dep-3b89d1da8075-postgres",
		now.Init("migrate"):        "apptown-init-dep-3b89d1da8075-migrate",
		now.Network():              "apptown-net-dep-3b89d1da8075",
		now.NeedVolume("postgres"): "apptown-need-dep-3b89d1da8075-postgres",
		now.DataVolume():           "apptown-data-apptown-dep-3b89d1da8075",
	}
	for got, want := range pins {
		if got != want {
			t.Errorf("name drifted: got %q, want %q", got, want)
		}
	}
	// The record decides: a deployment is named by its key, and one with no
	// id yet has no names.
	if n, err := NamesOf(id, "main"); err != nil || n != now {
		t.Errorf("NamesOf: %+v %v", n, err)
	}
	if _, err := NamesOf(ids.Nil, "main"); err == nil {
		t.Error("NamesOf named a deployment with no id")
	}
}

// Contract-regret item 1: two deployments whose project and deployment
// names join alike (shop/eu-main and shop-eu/main) shared every name, so
// one's release stopped the other's app or booted on its database. Named by
// key, they share none.
//
// Kills: a name built from the slug and deployment name, or from too few of
// the id's random digits.
func TestTwoDeploymentsWhoseNamesJoinAlikeShareNoName(t *testing.T) {
	t.Parallel()
	a, b := ByKey(keyed(1)), ByKey(keyed(2))
	pairs := [][2]string{{a.App(), b.App()}, {a.Stack(), b.Stack()}, {a.Service("postgres"), b.Service("postgres")},
		{a.Network(), b.Network()}, {a.NeedVolume("postgres"), b.NeedVolume("postgres")}, {a.DataVolume(), b.DataVolume()}, {a.Init("m"), b.Init("m")}}
	for _, p := range pairs {
		if p[0] == p[1] {
			t.Errorf("two deployments share %q", p[0])
		}
	}
	// No deployment's app is the start of another's (BuildImages lists a
	// container's tag and its -* forms), nor a preview's or a step's stack.
	if strings.HasPrefix(b.App(), a.App()) || isPreviewStackID(a.Stack()) || !strings.HasPrefix(a.Stack(), "dep-") {
		t.Errorf("names by key overlap another form: %s %s", a.App(), b.App())
	}
}

// The inverses read names AppTown gives now, and only those: a name made
// before 1.0 (forge-…) is not AppTown's.
//
// Kills: an inverse that still reads the forge- prefix.
func TestDockerNameInverses(t *testing.T) {
	t.Parallel()
	if id, ok := ParsePreviewChangeID("apptown-preview-change-00000000-0061-7000-8000-000000000000"); !ok || id != idstest.N(97) {
		t.Fatalf("parsePreviewChangeID: %s %v", id, ok)
	}
	for _, bad := range []string{"apptown-preview-change-", "apptown-preview-change-7", "apptown-preview-change-00000000-0007-7000-8000-000000000000x", "apptown-preview-run-00000000-0007-7000-8000-000000000000",
		"apptown-preview-change-00000000-0007-7000-8000-000000000000-old", "xapptown-preview-change-00000000-0007-7000-8000-000000000000", "apptown-preview-change-00000000-0000-0000-0000-000000000000",
		"forge-preview-change-00000000-0061-7000-8000-000000000000"} {
		if _, ok := ParsePreviewChangeID(bad); ok {
			t.Errorf("parsePreviewChangeID accepted %q", bad)
		}
	}
	if s, ok := StackOfNetwork("apptown-net-pvsnap-change-00000000-0007-7000-8000-000000000000"); !ok || s != "pvsnap-change-00000000-0007-7000-8000-000000000000" {
		t.Fatalf("stackOfNetwork: %q %v", s, ok)
	}
	for _, bad := range []string{"bridge", "forge-net-pvsnap-change-00000000-0007-7000-8000-000000000000"} {
		if _, ok := StackOfNetwork(bad); ok {
			t.Errorf("%s is not an AppTown network", bad)
		}
	}
	if c, ok := ContainerOfDataVolume("apptown-data-apptown-preview-run-00000000-0038-7000-8000-000000000000"); !ok || c != "apptown-preview-run-00000000-0038-7000-8000-000000000000" {
		t.Fatalf("containerOfDataVolume: %q %v", c, ok)
	}
	if _, ok := ContainerOfDataVolume("forge-data-forge-preview-run-00000000-0038-7000-8000-000000000000"); ok {
		t.Error("containerOfDataVolume read a forge- volume")
	}
	if !IsLocalBuildTag("apptown-preview-build-change-7") || IsLocalBuildTag("registry.example/app:1") || IsLocalBuildTag("forge-preview-build-change-7") {
		t.Error("isLocalBuildTag")
	}
	if !IsNamedVolume("apptown-need-dep-3b89d1da8075-postgres") || IsNamedVolume("forge-need-env-shop-main-postgres") || !Named("apptown-dep-3b89d1da8075") || Named("forge-env-shop-main") {
		t.Error("IsNamedVolume or Named")
	}
}

func TestNameInverses(t *testing.T) {
	t.Parallel()
	if l, ok := previewLabel("apptown-preview-letterbox-release-4"); !ok || l != "letterbox-release-4" {
		t.Fatalf("previewLabel: %q %v", l, ok)
	}
	for _, bad := range []string{"apptown-preview-", "forge-preview-letterbox-release-4"} {
		if _, ok := previewLabel(bad); ok {
			t.Errorf("%q is not a preview", bad)
		}
	}
	if !IsPreviewName("apptown-preview-run-1") || IsPreviewName("apptown-dep-3b89d1da8075") || IsPreviewName("apptown-preview-build-x") == false {
		// apptown-preview-build-* is an IMAGE tag, never a container name, so
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
	for _, id := range []string{"dep-3b89d1da8075", "123", "pv", "pvx", "pv12a", "env-pv1-main"} {
		if isPreviewStackID(id) {
			t.Errorf("%q must not be a preview stack", id)
		}
	}
}

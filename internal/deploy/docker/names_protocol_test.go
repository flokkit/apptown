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
// under Current; one made before its names were by key keeps every name it
// was made with, byte for byte, until it is moved.
func TestDockerNamesArePinned(t *testing.T) {
	t.Parallel()
	id := idstest.Parse(t, "01a15e84-c8f1-7000-b757-3b89d1da8075")
	now := ByKey(id)
	reading := BeforeKey(Before1, "reading-list", "main")
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
		// A deployment made before 1.0, not yet moved: today's live names.
		reading.Stack():                "env-reading-list-main",
		reading.App():                  "forge-env-reading-list-main",
		reading.Service("postgres"):    "forge-svc-env-reading-list-main-postgres",
		reading.Network():              "forge-net-env-reading-list-main",
		reading.Init("migrate"):        "forge-init-env-reading-list-main-migrate",
		reading.NeedVolume("postgres"): "forge-need-env-reading-list-main-postgres",
		reading.DataVolume():           "forge-data-forge-env-reading-list-main",
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
	// The record decides: no prefix is names by key, a recorded one the
	// names it was made with, anything else refused.
	if n, err := NamesOf(id, "", "reading-list", "main"); err != nil || n != now {
		t.Errorf("NamesOf with no prefix: %+v %v", n, err)
	}
	if n, err := NamesOf(id, "forge-", "reading-list", "main"); err != nil || n != reading {
		t.Errorf("NamesOf with forge-: %+v %v", n, err)
	}
	if _, err := NamesOf(id, "other-", "reading-list", "main"); err == nil {
		t.Error("NamesOf accepted a prefix AppTown never named with")
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
	if BeforeKey(Current, "shop", "eu-main").App() != BeforeKey(Current, "shop-eu", "main").App() {
		t.Fatal("the reproduction no longer reproduces: the old names do not collide")
	}
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

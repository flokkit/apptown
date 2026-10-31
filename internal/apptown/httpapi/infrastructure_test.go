package httpapi

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/deployment"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/containerresult/containerresulttest"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/docker"
	"github.com/flokkit/apptown/internal/deploy/portainer"
	"github.com/flokkit/apptown/internal/ids"
)

// A Docker host's leftovers are read by their labels: what AppTown made
// for a deployment is "left behind" by it with the reason; what belongs to
// an intranet stack is the intranet's, on this same daemon, not the host's.
func TestLeftoversAreAttributedByLabel(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	projects := project.New(db, nil)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "infra-app", Name: "Infra App"})
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := projects.ByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	d := deployment.New(context.Background(), db, deployment.Dependencies{})
	for _, target := range []*deployment.Target{{Name: "forge", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady}, {Name: "do-syd", Kind: deployment.KindDOApp, Origin: deployment.OriginAttached, Status: deployment.TargetReady}} {
		if _, err := d.Targets.Put(context.Background(), target); err != nil {
			t.Fatal(err)
		}
	}
	h := Infrastructure{deployment: d, projects: projects, db: db}
	if _, err := d.Targets.Put(context.Background(), &deployment.Target{Name: "lan", Kind: deployment.KindPortainer, Provider: "portainer-deploy", Origin: deployment.OriginAttached,
		Address: map[string]any{}, Capabilities: deployment.TargetCapabilities{}, Ingress: map[string]any{}, NeedsMode: "emulators", Status: deployment.TargetReady}); err != nil {
		t.Fatal(err)
	}
	host, _ := d.Targets.ByName(t.Context(), "forge")
	cloud, _ := d.Targets.ByName(t.Context(), "do-syd")
	cloudDep, err := d.Deployments.EnsureStanding(t.Context(), p.ID, "cloud", cloud.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	lan, _ := d.Targets.ByName(t.Context(), "lan")
	lanDep, err := d.Deployments.EnsureStanding(t.Context(), p.ID, "lan", lan.ID, deployment.Attributes{Feed: "manual", Audience: "organisation", DataClass: "non-production"})
	if err != nil {
		t.Fatal(err)
	}
	names, err := cloudDep.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	stack := portainer.StackName(portainer.StackNames(lanDep.ID))
	res := []deploy.Resource{
		{Kind: "container", Name: names.Service("postgres"), Record: "unrecorded", Owner: names.Stack()},
		{Kind: "volume", Name: stack + "_apptown-data-x", Record: "unrecorded", Stack: stack},
		{Kind: "container", Name: names.App(), Record: "recorded"},
	}
	if _, _, err := checkedLeftovers(t, h, host, res); err == nil {
		t.Fatal("legacy unverified stack was treated as removable or shared")
	}
	out, _, err := checkedLeftovers(t, h, host, []deploy.Resource{res[0], res[2]})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Retained == nil || out[0].Retained.Project != p.Slug || out[0].Retained.Deployment != "cloud" || !strings.Contains(out[0].Retained.Reason, "now runs on do-syd") {
		t.Fatalf("the cloud deployment's leftover names it and why: %+v", out[0].Retained)
	}
	if out[0].Record != "unrecorded" {
		t.Fatal("a leftover stays removable")
	}
}

// A leftover is attributed by its owner label to the deployment whose stack
// it names (dep-<key>). An owner no deployment has, including one a
// deployment made before 1.0 carried (env-<slug>-<name>), is left
// unattributed, never guessed.
//
// Kills: an owner label matched by its shape rather than by a deployment's
// recorded names.
func TestALeftoverIsAttributedToTheDeploymentItsOwnerLabelNames(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	projects := project.New(db, nil)
	id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
		return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: "shop", Name: "Shop"})
	})
	if err != nil {
		t.Fatal(err)
	}
	d := deployment.New(context.Background(), db, deployment.Dependencies{})
	host, err := d.Targets.Put(context.Background(), &deployment.Target{Name: "built-in-docker", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := d.Deployments.EnsureStanding(t.Context(), id, "eu-main", host.ID, deployment.Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	names, err := dep.HostNames()
	if err != nil {
		t.Fatal(err)
	}
	h := Infrastructure{deployment: d, projects: projects, db: db}
	res := []deploy.Resource{
		{Kind: "container", Name: names.Service("postgres"), Record: "unrecorded", Owner: names.Stack()},
		{Kind: "volume", Name: names.DataVolume(), Record: "unrecorded", Owner: names.App()},
		{Kind: "volume", Name: "forge-need-env-shop-eu-main-postgres", Record: "unrecorded", Owner: "env-shop-eu-main"},
		{Kind: "container", Name: "apptown-svc-dep-000000000000-x", Record: "unrecorded", Owner: "dep-000000000000"},
	}
	out, _, err := checkedLeftovers(t, h, host, res)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("every leftover is shown: %+v", out)
	}
	for i := range 2 {
		if r := out[i].Retained; r == nil || r.Project != "shop" || r.Deployment != "eu-main" {
			t.Errorf("%s (owner %s) is not attributed to shop/eu-main: %+v", out[i].Name, out[i].Owner, r)
		}
	}
	for _, r := range out[2:] {
		if r.Retained != nil {
			t.Errorf("an owner no deployment has (%s) is attributed: %+v", r.Owner, r.Retained)
		}
	}
}

// A record is "missing" only when an answer lacks it. With no answer yet
// (the first ask on its way) the records stay as recorded and the target
// is not live; an answer under a later error is still laid over them, but
// is not the latest (sweep 7: a live attached host read "15 missing, 0
// containers" while its list was being read).
func TestARecordIsMissingOnlyWhenAnAnswerLacksIt(t *testing.T) {
	t.Parallel()
	recorded := []deploy.Resource{{Kind: "container", Name: "apptown-env-app-main", Record: deploy.RecordRecorded}}
	running := []deploy.Resource{{Kind: "container", Name: "apptown-env-app-main"}}
	now := time.Now()
	for _, tc := range []struct {
		name             string
		live             []deploy.Resource
		errText          string
		at               time.Time
		record           string
		latest, answered bool
	}{
		{"no answer yet", nil, "", time.Time{}, deploy.RecordRecorded, false, false},
		{"the ask failed, nothing read before", nil, "docker ps: ssh refused", now, deploy.RecordRecorded, false, false},
		{"an answer", running, "", now, deploy.RecordRecorded, true, true},
		{"an empty host", nil, "", now, deploy.RecordMissing, true, true},
		{"an old answer under an error", running, "docker ps: ssh refused", now, deploy.RecordRecorded, false, true},
	} {
		out, latest, answered := deployment.InventoryAnswer(append([]deploy.Resource{}, recorded...), tc.live, tc.errText, tc.at, false)
		if len(out) != 1 || out[0].Record != tc.record || latest != tc.latest || answered != tc.answered {
			t.Fatalf("%s: %+v latest %v answered %v", tc.name, out, latest, answered)
		}
	}
}

// The inventory is one row per resource, not one row per inference/ownership
// receipt. This is the live demo's shape without any host-specific identifiers.
func TestInventoryReconciliationHasOneRowPerConfirmedResource(t *testing.T) {
	t.Parallel()
	var recorded, live []deploy.Resource
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("apptown-env-app-%d", i)
		h := deploy.ResourceHost{Project: "app", Deployment: "main", Generation: 3}
		live = append(live, deploy.Resource{Kind: "container", Name: name, Status: "active"})
		for j := 0; j < 4; j++ {
			id := ""
			if j > 0 {
				id = fmt.Sprintf("incarnation-%d-%d", i, j)
				if j == 3 {
					id = name
				}
			}
			recorded = append(recorded, deploy.Resource{Kind: "container", Name: name, ID: id, Hosts: []deploy.ResourceHost{h}})
		}
	}
	for _, kind := range []string{"volume", "network", "image"} {
		n := 4
		if kind == "image" {
			n = 9
		}
		for i := 0; i < n; i++ {
			name := fmt.Sprintf("%s-%d", kind, i)
			recorded = append(recorded, deploy.Resource{Kind: kind, Name: name, ID: name})
			live = append(live, deploy.Resource{Kind: kind, Name: name})
		}
	}
	out := deployment.MergeInventory(recorded, live)
	if recorded[1].ID != "incarnation-0-1" {
		t.Fatal("presentation altered teardown identity")
	}
	if len(out) != 27 {
		t.Fatalf("got %d rows, want 27 distinct resources", len(out))
	}
	for _, r := range out {
		if r.Record != deploy.RecordRecorded {
			t.Errorf("confirmed resource falsely missing: %+v", r)
		}
		if r.Kind == "container" && len(r.Hosts) != 1 {
			t.Errorf("duplicate hosting attribution: %+v", r)
		}
	}
	// Even without a provider answer, duplicate claims are still one resource.
	unassessed, latest, answered := deployment.InventoryAnswer(recorded, nil, "unavailable", time.Time{}, false)
	if len(unassessed) != 27 || latest || answered {
		t.Fatalf("unassessed inventory duplicated or confirmed: %d %v %v", len(unassessed), latest, answered)
	}
}

func TestInventoryPreservesDistinctProviderIdentitiesAndAllHosts(t *testing.T) {
	t.Parallel()
	a := deploy.ResourceHost{Project: "a", Deployment: "main", Generation: 1}
	b := deploy.ResourceHost{Project: "b", Deployment: "main", Generation: 2, Need: "postgres"}
	recorded := []deploy.Resource{
		{Kind: "volume", Name: "shared", Hosts: []deploy.ResourceHost{a}},
		{Kind: "volume", Name: "shared", ID: "shared", Hosts: []deploy.ResourceHost{b}},
		{Kind: "database", Name: "db", ID: "one"}, {Kind: "database", Name: "db", ID: "two"},
		{Kind: "image", Name: "reg/app", ID: "reg/app@sha256:one"}, {Kind: "image", Name: "reg/app", ID: "reg/app@sha256:two"},
	}
	live := []deploy.Resource{{Kind: "volume", Name: "shared"}, {Kind: "database", Name: "db", ID: "two"}, {Kind: "image", Name: "reg/app", ID: "reg/app@sha256:two"}, {Kind: "image", Name: "unrelated:latest", ID: "unrelated:latest"}}
	out := deployment.MergeInventory(recorded, live)
	if len(out) != 5 {
		t.Fatalf("identities collapsed or unrelated image exposed: %+v", out)
	}
	for _, r := range out {
		if r.Kind == "volume" && !slices.Equal(r.Hosts, []deploy.ResourceHost{a, b}) {
			t.Errorf("lost hosts: %+v", r)
		}
		if r.ID == "one" || r.ID == "reg/app@sha256:one" {
			if r.Record != deploy.RecordMissing {
				t.Errorf("different ID falsely confirmed: %+v", r)
			}
		}
		if r.ID == "two" || r.ID == "reg/app@sha256:two" {
			if r.Record != deploy.RecordRecorded {
				t.Errorf("exact ID not confirmed: %+v", r)
			}
		}
	}
}

func TestInventoryMatchesAnImmutableIDDespiteDisplayNameChanges(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"database", "image"} {
		for _, reverse := range []bool{false, true} {
			id := "provider-id"
			if kind == "image" {
				id = "reg/app@sha256:aaa"
			}
			recorded := deploy.Resource{Kind: kind, ID: id, Name: "friendly-name"}
			live := deploy.Resource{Kind: kind, ID: id, Name: id}
			if reverse {
				recorded, live = live, recorded
			}
			out := deployment.MergeInventory([]deploy.Resource{recorded}, []deploy.Resource{live})
			if len(out) != 1 || out[0].Record != deploy.RecordRecorded || out[0].ID != id {
				t.Errorf("%s reverse=%v: immutable ID not matched: %+v", kind, reverse, out)
			}
		}
	}
	// Real Docker image answers use the full reference for both name and ID.
	out := deployment.MergeInventory([]deploy.Resource{
		{Kind: "image", Name: "reg/app@sha256:aaa", ID: "reg/app@sha256:aaa"},
		{Kind: "image", Name: "reg/app@sha256:bbb", ID: "reg/app@sha256:bbb"},
	}, []deploy.Resource{{Kind: "image", Name: "reg/app@sha256:bbb", ID: "reg/app@sha256:bbb"}, {Kind: "image", Name: "reg/app:latest", ID: "reg/app:latest"}})
	if len(out) != 2 || out[0].Record != deploy.RecordMissing || out[1].Record != deploy.RecordRecorded {
		t.Fatalf("different digests or mutable tag falsely confirmed: %+v", out)
	}
}

// daemonReplies answers docker commands by their leading words.
type daemonReplies map[string]string

func (d daemonReplies) Run(_ context.Context, _ string, req docker.Command) ([]byte, error) {
	line := strings.Join(req.Args, " ")
	// A name filter keeps the names that contain it, as Docker's does.
	filter := ""
	for i, a := range req.Args {
		if a == "--filter" && i+1 < len(req.Args) {
			filter, _ = strings.CutPrefix(req.Args[i+1], "name=")
		}
	}
	for prefix, out := range d {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		var kept []string
		for _, l := range strings.SplitAfter(out, "\n") {
			if name, _, _ := strings.Cut(l, "\t"); l != "" && strings.Contains(name, filter) {
				kept = append(kept, l)
			}
		}
		if req.Stdout != nil {
			// A read with its own streams (image inspect) answers on stdout.
			_, err := containerresulttest.WriteString(req.Stdout, strings.Join(kept, ""))
			return nil, err
		}
		return []byte(strings.Join(kept, "")), nil
	}
	return nil, nil
}

// What AppTown records owning and the Docker host still holds reads as
// recorded, through the real reads: a local build recorded bare (BuildTag,
// which the daemon answers as name:latest) and an approved older database
// volume with Docker's anonymous name. A volume nobody records and AppTown
// did not name is not offered as an orphan.
func TestOwnedResourcesTheHostHoldsArePresent(t *testing.T) {
	t.Parallel()
	tag := docker.Current.BuildTag("apptown-env-shop-main-g1-app")
	anon := strings.Repeat("ab", 32)
	daemon := daemonReplies{
		"info":          "test-daemon\n",
		"image inspect": `["` + tag + `:latest"]` + "\t[]\t" + `"sha256:1"` + "\n",
		"volume ls":     anon + "\t\t\nsomeone-elses-data\t\t\n",
	}
	images := func(context.Context) ([]string, error) { return []string{tag}, nil }
	live, err := docker.Inventory{Scope: func(context.Context) (docker.Scope, error) { return docker.Scope{Client: daemon}, nil }, Images: images}.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	recorded := []deploy.Resource{
		{Kind: "image", Name: tag, ID: tag, Record: deploy.RecordRecorded},
		{Kind: "volume", Name: anon, ID: anon, Record: deploy.RecordRecorded},
	}
	out := deployment.MergeInventory(recorded, live)
	if len(out) != 2 {
		t.Fatalf("want the two owned resources and nothing else: %+v", out)
	}
	for _, r := range out {
		if r.Record != deploy.RecordRecorded || len(r.Tags) != 0 {
			t.Errorf("an owned resource the host holds read as %+v", r)
		}
	}
}

// "Torn down" stands beside leftovers an administrator may remove, so only
// an answer that the deployment is not there says it; a read that failed
// says the reason is not known.
func TestALeftoverIsCalledTornDownOnlyWhenItsDeploymentIsNotThere(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	d := deployment.New(context.Background(), db, deployment.Dependencies{})
	host, err := d.Targets.Put(context.Background(), &deployment.Target{Name: "built-in-docker", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	h := Infrastructure{deployment: d, projects: project.New(db, nil), db: db}
	if got := h.leftoverReason(t.Context(), host, "gone", "main"); got != "the deployment was torn down" {
		t.Fatalf("a project that is not there: %q", got)
	}
	failed, cancel := context.WithCancel(t.Context())
	cancel()
	if got := h.leftoverReason(failed, host, "gone", "main"); !strings.Contains(got, "could not be read") {
		t.Fatalf("a failed read: %q", got)
	}
}

func checkedLeftovers(t *testing.T, h Infrastructure, target *deployment.Target, resources []deploy.Resource) ([]deploy.Resource, string, error) {
	t.Helper()
	shared, err := h.deployment.Inventory.Shared(t.Context(), resources)
	if err != nil {
		return nil, "", err
	}
	return h.attributeLeftovers(t.Context(), target, resources, shared)
}

type removalInventory []deploy.Resource

func (i removalInventory) Inventory(context.Context) ([]deploy.Resource, error) { return i, nil }

func TestRemovalWithChangedProviderIdentityAsksToReload(t *testing.T) {
	t.Parallel()
	err := (Infrastructure{}).requireUnclaimedResource(t.Context(), removalInventory{{Kind: "container", Name: "app", ID: "new"}}, "container", "app", "old")
	if err == nil || !strings.Contains(err.Error(), "reload the page") || strings.Contains(err.Error(), "wait") {
		t.Fatalf("fresh identity refusal: %v", err)
	}
}

package deployment

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/dependency"
	"github.com/flokkit/apptown/internal/deploy"
	"github.com/flokkit/apptown/internal/deploy/appcontract"
)

// A part with no override and no declaration takes AppTown's default, the
// third and last level; a declaration and an explicit unlimited stand.
func TestFreshMemoryDefaultsKeepDeclarationsAndExplicitUnlimited(t *testing.T) {
	t.Parallel()
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}, {Name: "declared", Resources: appcontract.Resources{MemoryBytes: 64 << 20}}, {Name: "unlimited", Resources: appcontract.Resources{MemoryBytes: 128 << 20}}}, Needs: []string{"db", "key"}}
	a := AppliedProfile{Profile: "default", Parts: map[string]AppliedPart{"web": {Public: true}, "declared": {}, "unlimited": {MemoryUnlimited: true}, "db": {Need: true}}}
	e := &Environment{}
	out, applied, err := e.defaultMemory(t.Context(), c, a)
	if err != nil {
		t.Fatal(err)
	}
	if out.Parts[0].Resources.MemoryBytes != appTownDefaultMemory || applied.Parts["web"].MemoryDefault.Kind != "apptown" {
		t.Fatalf("AppTown's default witness: %+v", applied)
	}
	if out.Parts[1].Resources.MemoryBytes != 64<<20 || applied.Parts["declared"].MemoryDefault != nil {
		t.Fatal("default replaced declaration")
	}
	if out.Parts[2].Resources.MemoryBytes != 0 || !applied.Parts["unlimited"].MemoryUnlimited || applied.Parts["unlimited"].MemoryDefault != nil {
		t.Fatal("explicit unlimited became omitted/defaulted")
	}
	if out.NeedResources["db"].MemoryBytes != 512<<20 || applied.Parts["db"].MemoryDefault.Kind != "apptown" {
		t.Fatal("service need lost fallback")
	}
	if _, ok := out.NeedResources["key"]; ok {
		t.Fatal("key-only need was sized")
	}
	if c.Parts[0].Resources.MemoryBytes != 0 || len(c.NeedResources) != 0 {
		t.Fatal("defaulting changed caller's declaration")
	}
	raw, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := appliedFrom(raw)
	if err != nil {
		t.Fatal(err)
	}
	replay := saved.On(c)
	if replay.Parts[0].Resources.MemoryBytes != appTownDefaultMemory || replay.Parts[2].Resources.MemoryBytes != 0 {
		t.Fatal("replay lost default/unlimited facts")
	}
}

func TestACatalogueDefaultIsOnlyForItsActualNeed(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	catalog := dependency.NewCatalog(db, nil)
	if _, err := catalog.Save(t.Context(), "capability: db\ntitle: Database\nresources: {memory: 256M}\nlocal: {image: postgres:16}", "tester"); err != nil {
		t.Fatal(err)
	}
	e := &Environment{needs: catalog}
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}}, Needs: []string{"db"}}
	a := AppliedProfile{Profile: "default", Parts: map[string]AppliedPart{"web": {Public: true}, "db": {Need: true}}}
	out, applied, err := e.defaultMemory(t.Context(), c, a)
	if err != nil {
		t.Fatal(err)
	}
	if out.NeedResources["db"].MemoryBytes != 256<<20 || applied.Parts["db"].MemoryDefault.Name != "db" {
		t.Fatal("catalogue default not applied to need")
	}
	svcs, record, err := SuppliedServiceDefaults(&appcontract.Contract{}, []deploy.ServiceSpec{{Name: "db", Image: "postgres:16"}})
	if err != nil {
		t.Fatal(err)
	}
	if svcs[0].Resources.MemoryBytes != 512<<20 || record["db"].MemoryDefault.Kind != "apptown" {
		t.Fatal("supplied image/name was guessed as a catalogue capability")
	}
}

func TestSuppliedServiceDefaultsRefuseAmbiguousStackAndKeepPositiveLimits(t *testing.T) {
	t.Parallel()
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web"}}, Needs: []string{"postgres"}}
	for _, name := range []string{"web", "postgres", ""} {
		if _, _, err := SuppliedServiceDefaults(c, []deploy.ServiceSpec{{Name: name}}); err == nil {
			t.Fatalf("ambiguous identity %q allowed", name)
		}
	}
	svcs, a, err := SuppliedServiceDefaults(c, []deploy.ServiceSpec{{Name: "cache", Resources: appcontract.Resources{MemoryBytes: 64 << 20}}, {Name: "jobs", Resources: appcontract.Resources{CPUs: .5}}})
	if err != nil {
		t.Fatal(err)
	}
	if svcs[0].Resources.MemoryBytes != 64<<20 || a["cache"].MemoryDefault != nil || svcs[1].Resources.MemoryBytes != 512<<20 || svcs[1].Resources.CPUs != .5 {
		t.Fatal("supplied declaration/default precedence")
	}
}

func TestExplicitUnlimitedOverrideIsAnActDistinctFromClear(t *testing.T) {
	t.Parallel()
	w, _ := profiledWorld(t)
	b := w.publish(t)
	if _, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "prod", w.lab.ID, Attributes{Feed: "promotion", Audience: "team", DataClass: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.promote(t, b.ID, "prod", w.lab); err != nil {
		t.Fatal(err)
	}
	change := OverrideChange{ProjectID: w.project, Deployment: "prod", Part: "web", MemoryUnlimited: true, Reason: "measured external supervisor"}
	if _, err := w.s.SetOverride(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	dep, err := w.s.Deployments.ByName(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	overrides, err := w.s.Overrides(t.Context(), dep.ID)
	if err != nil || !overrides["web"].MemoryUnlimited {
		t.Fatalf("unlimited choice was cleared: %+v %v", overrides, err)
	}
	// The queued override redeploy is executed through the ordinary release path.
	rel, err := w.s.Releases.Last(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.s.ExecuteRelease(t.Context(), rel.ID); err != nil {
		t.Fatal(err)
	}
	a, err := w.s.SettledApplied(t.Context(), dep)
	if err != nil {
		t.Fatal(err)
	}
	dep, err = w.s.Deployments.ByName(t.Context(), w.project, "prod")
	if err != nil {
		t.Fatal(err)
	}
	a, err = w.s.SettledApplied(t.Context(), dep)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Parts["web"].MemoryUnlimited || a.Parts["web"].Resources.MemoryBytes != 0 {
		t.Fatalf("applied unlimited: %+v", a)
	}
	cmd := ranWith(t, w, "lab", w.names(t, "prod").App(), len(w.hosts.ran("lab"))-1)
	if strings.Contains(cmd, "--memory ") {
		t.Fatalf("unlimited was defaulted: %s", cmd)
	}
	change.MemoryUnlimited = false
	if _, err := w.s.SetOverride(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	overrides, err = w.s.Overrides(t.Context(), dep.ID)
	if err != nil || len(overrides) != 0 {
		t.Fatalf("clear didn't remove explicit choice: %+v %v", overrides, err)
	}
}

func TestAppPlatformKeepsManagedNeedSizesWithoutDefaults(t *testing.T) {
	t.Parallel()
	w, _ := profiledWorld(t)
	target, err := w.s.Targets.Put(t.Context(), &Target{Name: "managed-defaults", Kind: KindDOApp, Origin: OriginAttached, Status: TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := w.s.Deployments.EnsureStanding(t.Context(), w.project, "managed", target.ID, Attributes{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	// An actual service kind is known, but provider-managed database sizing is
	// neither its local emulator's default nor AppTown's container fallback.
	w.s.env.needs = dependency.NewCatalog(w.db, nil)
	if _, err := w.s.env.needs.Save(t.Context(), "capability: manageddb\ntitle: Database\nresources: {memory: 256M}\nlocal: {image: postgres:16}", "tester"); err != nil {
		t.Fatal(err)
	}
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true, Image: "app:1"}}, Needs: []string{"manageddb"}}
	out, a, err := w.s.effectiveContract(t.Context(), dep, t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.NeedResources) != 0 || out.Parts[0].Resources.MemoryBytes != 0 || a.Parts["manageddb"].MemoryDefault != nil {
		t.Fatalf("container default leaked to managed provider: %+v %+v", out, a)
	}
}

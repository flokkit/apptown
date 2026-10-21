package deployment

import (
	"context"
	"encoding/json"
	"errors"
	"flokkit.io/forge/internal/deploy"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/deploy/docker"
	"flokkit.io/forge/internal/forge/dependency"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
	"strings"
	"testing"
)

type defaultPack struct {
	memory string
	err    error
}

func (p defaultPack) MemoryDefault(context.Context, ids.ID) (appcontract.MemoryDefault, string, int64, error) {
	return appcontract.MemoryDefault{Memory: p.memory}, "chosen", 3, p.err
}

func TestFreshMemoryDefaultsKeepDeclarationsAndExplicitUnlimited(t *testing.T) {
	t.Parallel()
	c := &appcontract.Contract{Parts: []appcontract.Part{{Name: "web", Public: true}, {Name: "declared", Resources: appcontract.Resources{MemoryBytes: 64 << 20}}, {Name: "unlimited", Resources: appcontract.Resources{MemoryBytes: 128 << 20}}}, Needs: []string{"db", "key"}}
	a := AppliedProfile{Profile: "default", Parts: map[string]AppliedPart{"web": {Public: true}, "declared": {}, "unlimited": {MemoryUnlimited: true}, "db": {Need: true}}}
	e := &Environment{memoryDefaults: defaultPack{memory: "160M"}}
	out, applied, err := e.DefaultMemory(t.Context(), idstest.N(1), c, a)
	if err != nil {
		t.Fatal(err)
	}
	if out.Parts[0].Resources.MemoryBytes != 160<<20 || applied.Parts["web"].MemoryDefault.Kind != "pack" || applied.Parts["web"].MemoryDefault.Version != 3 {
		t.Fatalf("pack fallback witness: %+v", applied)
	}
	if out.Parts[1].Resources.MemoryBytes != 64<<20 || applied.Parts["declared"].MemoryDefault != nil {
		t.Fatal("default replaced declaration")
	}
	if out.Parts[2].Resources.MemoryBytes != 0 || !applied.Parts["unlimited"].MemoryUnlimited || applied.Parts["unlimited"].MemoryDefault != nil {
		t.Fatal("explicit unlimited became omitted/defaulted")
	}
	if out.NeedResources["db"].MemoryBytes != 512<<20 || applied.Parts["db"].MemoryDefault.Kind != "forge" {
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
	if replay.Parts[0].Resources.MemoryBytes != 160<<20 || replay.Parts[2].Resources.MemoryBytes != 0 {
		t.Fatal("replay lost default/unlimited facts")
	}
	e.memoryDefaults = defaultPack{err: errors.New("publication unreadable")}
	if _, _, err := e.DefaultMemory(t.Context(), idstest.N(1), c, a); err == nil {
		t.Fatal("unreadable publication silently fell back")
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
	out, applied, err := e.DefaultMemory(t.Context(), idstest.N(1), c, a)
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
	if svcs[0].Resources.MemoryBytes != 512<<20 || record["db"].MemoryDefault.Kind != "forge" {
		t.Fatal("supplied image/name was guessed as a catalogue capability")
	}
}

func TestALandingCapsAndRecordsSuppliedWorkflowServices(t *testing.T) {
	t.Parallel()
	w := noLocalDockerWorld(t, multipartApp)
	s := w.service(false)
	dep := w.deployment(t, s, "main", w.remote)
	supplied := []deploy.ServiceSpec{{Name: "sidecar", Image: "redis:7"}}
	art, cleanup, err := docker.PrepareSource(t.Context(), w.repo, w.revision, "game", supplied)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	out, err := s.RunLanding(t.Context(), landingClaim{w.project, idstest.N(7)}, LandingInput{Start: Start{DeploymentID: dep.ID, TargetID: dep.TargetID, Key: "supplied", Inputs: Inputs{ReleaseRun: idstest.N(7), Revision: w.revision, ArtifactHash: art.Hash, DockerSource: string(art.Body)}}, Source: *art.Source, Services: supplied, DataClass: "synthetic"})
	if err != nil || out.Boot.Err != nil || out.Execution.Outcome != Applied {
		t.Fatalf("landing: %+v %v", out, err)
	}
	container := docker.Current.Service(docker.StackID("game", "main"), "sidecar")
	if memory, _ := w.daemon.Limits(container); memory != 512<<20 {
		t.Fatalf("supplied standing service limit: %d commands%v", memory, w.daemon.Commands())
	}
	stored, err := s.Deployments.ByID(t.Context(), dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.SettledApplied(t.Context(), stored)
	if err != nil {
		t.Fatal(err)
	}
	if a.Services["sidecar"].Resources.MemoryBytes != 512<<20 || a.Services["sidecar"].MemoryDefault.Kind != "forge" {
		t.Fatalf("unrecorded supplied service: %+v", a)
	}
	s.authority = &requestAuthority{}
	view, err := s.Resources(t.Context(), w.project, "main")
	if err != nil {
		t.Fatal(err)
	}
	seenSidecar, seenApp := false, false
	for _, row := range view.Parts {
		if row.Part == "sidecar" {
			seenSidecar = row.SuppliedService && row.Memory == "512Mi" && row.Observed.Part == "sidecar"
		}
		if row.Public {
			seenApp = !row.SuppliedService
		}
	}
	if !seenSidecar || !seenApp {
		t.Fatalf("supplied service override distinction lost: %+v", view.Parts)
	}
	containers := partContainers(stored, "game", a)
	if containers["sidecar"] != container {
		t.Fatalf("supplied observation attributed to %q", containers["sidecar"])
	}
	promised := sumDeclared([]AppliedProfile{a})
	if promised[0].Limited.Forge != len(a.Parts)+1 || promised[0].Unlimited.Forge != 0 {
		t.Fatalf("supplied cap omitted from capacity: %+v", promised[0])
	}
	s.env.memoryDefaults = defaultPack{memory: "1G"}
	w.daemon.remove(stored.Container)
	if _, err := w.rebuild(t, s, stored); err != nil {
		t.Fatalf("rebuild supplied services: %v", err)
	}
	if memory, _ := w.daemon.Limits(container); memory != 512<<20 {
		t.Fatalf("supplied replay changed cap to today's default: %d", memory)
	}
	if memory, _ := w.daemon.Limits(stored.Container); memory != 512<<20 {
		t.Fatalf("app replay changed cap to today's pack default: %d", memory)
	}
	if got := a.OnServices(supplied); got[0].Resources.MemoryBytes != 512<<20 {
		t.Fatal("supplied service replay used omitted source limit")
	}
	if got := (AppliedProfile{}).OnServices(supplied); got[0].Resources.MemoryBytes != 0 {
		t.Fatal("legacy replay gained a new cap")
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
	cmd := ranWith(t, w, "lab", docker.Current.Container("p", "prod"), len(w.hosts.ran("lab"))-1)
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
	// neither its local emulator's default nor Forge's container fallback.
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

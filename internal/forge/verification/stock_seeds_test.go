package verification

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"flokkit.io/forge/internal/check"
	"flokkit.io/forge/internal/deploy/appcontract"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/secret"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/stock"
)

// A new project starts as its pack's seed, and the first thing a review of
// it sees is that tree. Every shell check the pack (and the platform tier
// every project is on) declares must pass on the seed as AppTown writes
// it — the repository's own ignore lines first, then the seed's — or each
// first change inherits findings it did not cause and is sent off to fix
// the starter (root Dockerfiles, missing ignore rules).
func TestSeedsPassTheirPacksOwnShellChecks(t *testing.T) {
	t.Parallel()
	packs, err := wow.ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	var platform []string
	for _, p := range packs {
		if p.Platform {
			platform = append(platform, p.Guidance...)
		}
	}
	items, _ := wow.ReadStockGuidance(stock.FS, nil)
	checksOf := map[string][]conformance.CheckDeclaration{}
	for _, it := range items {
		var cs []conformance.CheckDeclaration
		if it.ChecksJSON != "" {
			if err := json.Unmarshal([]byte(it.ChecksJSON), &cs); err != nil {
				t.Fatalf("%s: %v", it.Name, err)
			}
		}
		checksOf[it.Name] = cs
	}
	evaluator := NewEvaluator(localChecks{}, nil)
	seeded := 0
	for _, p := range packs {
		root := "seeds/" + p.Name
		if _, err := fs.Stat(stock.FS, root); err != nil {
			continue
		}
		seeded++
		dir := t.TempDir()
		// What the repository init writes before the seed arrives.
		writeTree(t, dir, map[string]string{".gitignore": ".apptown/*\n!.apptown/app.yaml\nnode_modules/\n"})
		if err := fs.WalkDir(stock.FS, root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, err := stock.FS.ReadFile(path)
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(path, root+"/")
			if rel == ".gitignore" {
				prior, _ := os.ReadFile(filepath.Join(dir, rel))
				body = append(append(prior, '\n'), body...)
			}
			writeTree(t, dir, map[string]string{rel: string(body)})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var checks []conformance.DeclaredCheck
		for _, block := range append(append([]string{}, platform...), p.Guidance...) {
			for _, c := range checksOf[block] {
				if c.Evaluator == "sh" {
					checks = append(checks, conformance.DeclaredCheck{Block: block, Version: 1, Check: c})
				}
			}
		}
		results, _, errs := evaluator.runShChecks(context.Background(), checks, dir, "t-seed-"+p.Name)
		if errs != 0 {
			t.Errorf("%s: %d checks errored", p.Name, errs)
		}
		for _, r := range results {
			if r.Verdict != workflow.VerdictPass {
				t.Errorf("seed %s fails %s · %s: %s", p.Name, r.Block, r.Check, strings.TrimSpace(r.Output))
			}
		}
	}
	if seeded < 8 {
		t.Fatalf("expected the stock packs' seeds, found %d", seeded)
	}
}

// A seed's runtime contract is what a new project's first round reads: the
// needs it declares are the ones the implementing agent gets as emulators
// before any app exists. Each must parse as AppTown reads it at boot.
func TestSeedContractsParse(t *testing.T) {
	t.Parallel()
	paths, err := fs.Glob(stock.FS, "seeds/*/.apptown/app.yaml")
	if err != nil || len(paths) < 8 {
		t.Fatalf("expected the stock seeds' contracts, found %d (%v)", len(paths), err)
	}
	for _, path := range paths {
		raw, err := stock.FS.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		c, err := appcontract.Parse(raw)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		// A seed without a version is read as version 1, whose sizes the
		// version 2 contract a project upgrades to refuses.
		if c.Version != 2 {
			t.Errorf("%s: contract version %d, want 2", path, c.Version)
		}
	}
}

// A seed's secret declaration is what makes AppTown generate the keys
// its settings refuse to boot without (django-htmx's DJANGO_SECRET_KEY).
// Each must parse as AppTown reads it at deploy, and declare nothing the
// AppTown ignores.
func TestSeedSecretDeclarationsParse(t *testing.T) {
	t.Parallel()
	paths, err := fs.Glob(stock.FS, "seeds/*/secretspec.toml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("expected the stock seeds' secret declarations, found %d (%v)", len(paths), err)
	}
	for _, path := range paths {
		raw, err := stock.FS.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := secret.ParseSpec(raw)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if len(spec.Ignored) > 0 {
			t.Errorf("%s declares what AppTown ignores: %v", path, spec.Ignored)
		}
	}
}

// A pack that proposes a service (Postgres) promises the app gets one. The
// seed's runtime contract is what provisions it at deploy, so a seed that
// leaves the need out ships an app whose database exists only in the
// change chat (deployed with no DATABASE_URL).
func TestSeedsDeclareTheServicesTheirPacksPropose(t *testing.T) {
	t.Parallel()
	packs, err := wow.ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	compared := 0
	for _, p := range packs {
		raw, err := stock.FS.ReadFile("seeds/" + p.Name + "/.apptown/app.yaml")
		if err != nil {
			continue
		}
		seed, err := appcontract.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		for _, svc := range p.Services {
			compared++
			found := false
			for _, n := range seed.Needs {
				found = found || n == svc.Name
			}
			if !found {
				t.Errorf("pack %s proposes %s; its seed's .apptown/app.yaml does not declare it under needs", p.Name, svc.Name)
			}
		}
	}
	if compared == 0 {
		t.Fatal("no seeded pack proposes a service; the check has nothing to compare")
	}
}

// A pack's guidance that shows a runtime contract with an init step (the
// migration a database app needs before it serves) describes the contract
// the seed ships. A seed that declares the database but leaves the init
// step for the agent to add boots a cold container with no schema, and a
// Rails app answers every page with PG::UndefinedTable.
func TestSeedsShipTheInitStepTheirGuidanceShows(t *testing.T) {
	t.Parallel()
	packs, err := wow.ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := wow.ReadStockGuidance(stock.FS, nil)
	bodies := map[string]string{}
	for _, it := range items {
		bodies[it.Name] = it.Body
	}
	shown := 0
	for _, p := range packs {
		raw, err := stock.FS.ReadFile("seeds/" + p.Name + "/.apptown/app.yaml")
		if err != nil {
			continue
		}
		seed, err := appcontract.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		for _, block := range p.Guidance {
			for _, example := range indentedContracts(bodies[block]) {
				want, err := appcontract.Parse([]byte(example))
				if err != nil || len(want.Init) == 0 {
					continue
				}
				shown++
				if !reflect.DeepEqual(seed.Init, want.Init) {
					t.Errorf("seed %s ships init %+v; its guidance %s shows %+v", p.Name, seed.Init, block, want.Init)
				}
			}
		}
	}
	if shown == 0 {
		t.Fatal("no pack guidance shows an init step; the check has nothing to compare")
	}
}

// indentedContracts returns each four-space-indented block of a guidance
// body that begins with `app:`, unindented: the runtime contracts it shows.
func indentedContracts(body string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 && cur[0] == "app:" {
			out = append(out, strings.Join(cur, "\n")+"\n")
		}
		cur = nil
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "    ") {
			cur = append(cur, strings.TrimPrefix(line, "    "))
			continue
		}
		if strings.TrimSpace(line) == "" && len(cur) > 0 {
			cur = append(cur, "")
			continue
		}
		flush()
	}
	flush()
	return out
}

// A pack whose seed builds in one image and runs in another has stated
// which image is its build image. Its runtime-stage-not-build-image check
// must refuse exactly that image as the final stage, so the check's image
// name is the seed's declaration, not a separate list. Seeds whose final
// stage is the build image's own family (node, python, ruby) or a scratch
// carrier of files (AppTown serves them; the image never runs) state no
// such split.
func TestSeedBuildImagesAreRefusedAsTheirRuntime(t *testing.T) {
	t.Parallel()
	packs, err := wow.ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := wow.ReadStockGuidance(stock.FS, nil)
	checksOf := map[string][]conformance.CheckDeclaration{}
	for _, it := range items {
		var cs []conformance.CheckDeclaration
		if it.ChecksJSON != "" {
			if err := json.Unmarshal([]byte(it.ChecksJSON), &cs); err != nil {
				t.Fatalf("%s: %v", it.Name, err)
			}
		}
		checksOf[it.Name] = cs
	}
	evaluator := NewEvaluator(localChecks{}, nil)
	split := 0
	for _, p := range packs {
		raw, err := stock.FS.ReadFile("seeds/" + p.Name + "/Dockerfile")
		if err != nil {
			continue
		}
		lines := strings.Split(string(raw), "\n")
		var froms []int
		for i, l := range lines {
			if f := strings.Fields(l); len(f) > 1 && strings.EqualFold(f[0], "FROM") {
				froms = append(froms, i)
			}
		}
		if len(froms) < 2 {
			continue
		}
		build := strings.Fields(lines[froms[0]])[1]
		final := strings.Fields(lines[froms[len(froms)-1]])[1]
		if imageRepository(final) == "scratch" || imageRepository(final) == imageRepository(build) {
			continue
		}
		split++
		var checks []conformance.DeclaredCheck
		for _, block := range p.Guidance {
			for _, c := range checksOf[block] {
				if c.Name == "runtime-stage-not-build-image" {
					checks = append(checks, conformance.DeclaredCheck{Block: block, Version: 1, Check: c})
				}
			}
		}
		if len(checks) == 0 {
			t.Errorf("seed %s builds FROM %s and runs FROM %s, but no block of the pack declares runtime-stage-not-build-image", p.Name, build, final)
			continue
		}
		lines[froms[len(froms)-1]] = "FROM " + build
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{"Dockerfile": strings.Join(lines, "\n")})
		results, _, errs := evaluator.runShChecks(context.Background(), checks, dir, "t-seed-runtime-"+p.Name)
		refused := false
		for _, r := range results {
			refused = refused || r.Verdict == workflow.VerdictFail
		}
		if !refused || errs != 0 {
			t.Errorf("seed %s with its final stage FROM %s passes its pack's runtime-stage-not-build-image: %+v", p.Name, build, results)
		}
	}
	if split < 5 {
		t.Fatalf("expected the compiled stacks' seeds (rust, go, java, dotnet, godot), found %d", split)
	}
}

// imageRepository is an image reference without its tag, digest or Docker
// Hub prefix, as the runtime-stage-not-build-image checks compare it.
func imageRepository(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	if slash := strings.LastIndex(ref, "/"); strings.LastIndex(ref, ":") > slash {
		ref = ref[:strings.LastIndex(ref, ":")]
	}
	ref = strings.TrimPrefix(ref, "docker.io/")
	return strings.TrimPrefix(ref, "library/")
}

// Every pack's seed meets the platform's memory-declared check as AppTown
// judges it, so a new project starts with each built part held to a size: a
// server seed declares its memory, a static seed has nothing to declare.
func TestSeedsDeclareTheirMemory(t *testing.T) {
	t.Parallel()
	packs, err := wow.ReadStockPacks(stock.FS)
	if err != nil {
		t.Fatal(err)
	}
	seeded := 0
	for _, p := range packs {
		raw, err := fs.ReadFile(stock.FS, "seeds/"+p.Name+"/.apptown/app.yaml")
		if err != nil {
			continue
		}
		seeded++
		c, err := appcontract.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if v := judgeMemoryDeclared(c); v.Outcome != check.Conforms {
			t.Errorf("seed %s: %s", p.Name, v.Detail)
		}
	}
	if seeded == 0 {
		t.Fatal("no seed was read")
	}
}

package forge

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/deployment"
	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
)

// With a standing deployment missing, the production evaluator says so
// (cannot-assess), never pass — and the default probe IS wired.
func TestRuntimeEvaluatorWithoutAnDeployment(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	pid, _, _ := fleetRepo(t, a, "rt", map[string]string{"a.txt": "x"})
	st, why := a.evaluator.Observe(t.Context(), pid, "", "readiness")
	if st != conformance.CannotAssess || !strings.Contains(why, "no standing main deployment") {
		t.Fatalf("no env → cannot-assess with the reason: %s %q", st, why)
	}
	if st, _ := a.evaluator.Observe(t.Context(), pid, "", "nonsense"); st != conformance.EvaluationError {
		t.Fatalf("a bad probe spec is an evaluator error, got %s", st)
	}
}

// A review judges the contract check at the commit it recorded, deployed or
// not: a commit landed since does not answer for the one reviewed.
func TestAReviewJudgesTheContractAtItsRecordedCommit(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	pid, declared, git := fleetRepo(t, a, "mem", map[string]string{".apptown/app.yaml": "app:\n  web:\n    build: .\n    ports: [\"8080\"]\n    public: true\n    resources: { memory: 64M }\n"})
	if st, why := a.evaluator.Observe(t.Context(), pid, declared, "memory-declared"); st != conformance.Conforms {
		t.Fatalf("the declared commit, never deployed: %s %q", st, why)
	}
	git("rm", "-q", ".apptown/app.yaml")
	git("commit", "-q", "-m", "drop the contract")
	if st, _ := a.evaluator.Observe(t.Context(), pid, declared, "memory-declared"); st != conformance.Conforms {
		t.Fatalf("a later commit answered for the reviewed one: %s", st)
	}
	if st, why := a.evaluator.Observe(t.Context(), pid, "", "memory-declared"); st != conformance.CannotAssess || !strings.Contains(why, "no .apptown/app.yaml") {
		t.Fatalf("the default branch as it stands: %s %q", st, why)
	}
}

// The platform tier reaches a project through the apptown pack; at boot the
// stock sync never appends a platform block to an existing project — that
// is an adoption (design/subsystems/ways-of-working-versions.md §9 step 1).
func TestPlatformGuidanceIsNotAppendedAtBoot(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t)
	repoProj, _, _ := fleetRepo(t, a, "withrepo", map[string]string{"a.txt": "x"})
	shipped := fstest.MapFS{"guidance/deploy/x.md": &fstest.MapFile{Data: []byte("---\nname: deploy/x\ntier: platform\n---\nfirst version\n")}}
	publisher := wow.New(a.DB, wow.Dependencies{Stock: shipped})
	if counts := publisher.SyncStockGuidance(t.Context()); counts.Added != 1 || counts.Errors != 0 {
		t.Fatalf("publish first platform block: %+v", counts)
	}
	shipped["guidance/deploy/x.md"].Data = []byte("---\nname: deploy/x\ntier: platform\n---\nsecond version\n")
	if counts := publisher.SyncStockGuidance(t.Context()); counts.Updated != 1 || counts.Errors != 0 {
		t.Fatalf("publish second platform version: %+v", counts)
	}
	block, err := a.Ways.BlockByName(t.Context(), "deploy/x")
	if err != nil {
		t.Fatal(err)
	}
	bid := block.ID
	// The fixture's way onto the platform tier. Not ensurePlatformGuidanceFor:
	// the app's own release does not ship deploy/x, so it would not offer it.
	if err := a.Adoption.AppendPlatformImport(t.Context(), repoProj, *block); err != nil {
		t.Fatal(err)
	}
	imports := func(pid, block ids.ID) (n, ver int) {
		all, err := a.Adoption.Imports(t.Context(), pid)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range all {
			if im.BlockID == block {
				n++
				ver = max(ver, im.Version)
			}
		}
		return
	}
	count := func(pid ids.ID) (n, ver int) { return imports(pid, bid) }
	if n, v := count(repoProj); n != 1 || v != 2 {
		t.Fatalf("fixture: the platform block imported once at v2: n=%d v=%d", n, v)
	}
	// A second platform block published later does not appear on the
	// existing project by itself.
	shipped["guidance/deploy/y.md"] = &fstest.MapFile{Data: []byte("---\nname: deploy/y\ntier: platform\n---\nlater platform block\n")}
	if counts := publisher.SyncStockGuidance(t.Context()); counts.Added != 1 || counts.Errors != 0 {
		t.Fatalf("publish later platform block: %+v", counts)
	}
	laterBlock, err := a.Ways.BlockByName(t.Context(), "deploy/y")
	if err != nil {
		t.Fatal(err)
	}
	a.Ways.SyncStockGuidance(t.Context())
	if n, _ := count(repoProj); n != 1 {
		t.Fatalf("the platform import count is unchanged by a boot sync: %d", n)
	}
	if later, _ := imports(repoProj, laterBlock.ID); later != 0 {
		t.Fatalf("a platform block published later is not appended at boot (adoption is frozen), got %d", later)
	}
}

// At the gate a runtime check is judged against the change's preview, and
// with no preview it is cannot-assess with the reason — never a pass, and
// never the standing deployment (which runs the code the change replaces).

// A gate that could not probe for AppTown's own reasons must not send the
// implementer to rework: it is an error waiting for a person, not a fail.

// A served app that declares no readiness path has not met the way of
// working: the verdict is does-not-conform with the words to fix it, never
// "cannot assess", which is reserved for AppTown's own inability.

// A main deployment whose container is not running because the app
// declares no contract is the app's gap: does-not-conform, with the words
// to fix it. A project never deployed stays "cannot assess".
func TestMainDeploymentWithoutAContractDoesNotConform(t *testing.T) {
	t.Parallel()
	a := conformanceTestApp(t, Config{DockerClient: newFakeDocker()})
	pid, _, _ := fleetRepo(t, a, "nc", map[string]string{"README.md": "# no contract"})
	target, err := a.Deployment.Targets.Put(context.Background(), &deployment.Target{Name: "runtime", Kind: deployment.KindDockerHost, Origin: deployment.OriginBuiltIn, Status: deployment.TargetReady})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.EnsureStanding(t.Context(), pid, "main", target.ID, (deployment.Attributes{}).WithDefaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Deployment.Deployments.ReserveHostPort(t.Context(), pid, "main", "apptown-env-nc-main", 40001); err != nil {
		t.Fatal(err)
	}
	state, detail := a.evaluator.Observe(t.Context(), pid, "", "readiness")
	if state != conformance.DoesNotConform || !strings.Contains(detail, "health.readiness.http") {
		t.Fatalf("want does-not-conform naming the contract, got %s: %s", state, detail)
	}
	pid2, _, _ := fleetRepo(t, a, "nd", map[string]string{"README.md": "# never deployed"})
	if state, _ := a.evaluator.Observe(t.Context(), pid2, "", "readiness"); state != conformance.CannotAssess {
		t.Fatalf("a project never deployed is not judged at runtime: %s", state)
	}
}

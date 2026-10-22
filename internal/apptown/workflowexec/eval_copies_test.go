package workflowexec

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/integration"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func carriesCanary(env []string) bool {
	return slices.ContainsFunc(env, func(kv string) bool { return strings.Contains(kv, stepSecretCanary) })
}

// An eval copy's agent step is given no instance step value, on this
// AppTown's host and on a worker, where an ordinary project's is given the
// canary.
func TestACopysAgentStepIsGivenNoInstanceStepValue(t *testing.T) {
	f := agentOnWorkerFixture(t, nil)
	if req := f.build(t); !carriesCanary(req.spec.Env) {
		t.Fatalf("the canary reaches an ordinary project's worker step: %v", req.spec.Env)
	}
	run, step := f.call.run, f.call.step
	if _, err := f.exec.integrations.Bindings.Upsert(t.Context(), "registry", integration.CapContainerRegistry, "service", `{"url":"registry.example"}`, false); err != nil {
		t.Fatal(err)
	}
	if err := f.exec.integrations.Attachments.Attach(t.Context(), run.ProjectID, integration.CapContainerRegistry, "registry", `{"repo":"registry.example/team/agents","username":"robot","token":"registry-canary"}`, integration.Entered, "test", nil); err != nil {
		t.Fatal(err)
	}
	if reg, err := f.exec.stepRegistry(t.Context(), run.ProjectID); err != nil || reg == nil {
		t.Fatalf("an ordinary project's private Docker is logged in to its registry: %+v %v", reg, err)
	}
	env, _, err := f.exec.agentEnv(t.Context(), run, step, &agentConfig{}, t.TempDir(), "fake/model", agentKnobs{executor: execution.Local})
	if err != nil || !carriesCanary(env) {
		t.Fatalf("the canary reaches an ordinary project's local step: %v %v", env, err)
	}

	f = agentOnWorkerFixtureFor(t, project.EvalCopy, nil)
	run, step = f.call.run, f.call.step
	if _, err := f.exec.integrations.Bindings.Upsert(t.Context(), "registry", integration.CapContainerRegistry, "service", `{"url":"registry.example"}`, false); err != nil {
		t.Fatal(err)
	}
	if req := f.build(t); carriesCanary(req.spec.Env) {
		t.Fatalf("a copy's worker step was given the canary: %v", req.spec.Env)
	}
	env, _, err = f.exec.agentEnv(t.Context(), run, step, &agentConfig{}, t.TempDir(), "fake/model", agentKnobs{executor: execution.Local})
	if err != nil || carriesCanary(env) {
		t.Fatalf("a copy's local step was given the canary: %v %v", env, err)
	}
	// A copy can hold no attachment, so the guard is witnessed where it
	// decides: a project whose purpose cannot be read is given nothing and
	// the step is refused.
	if reg, err := f.exec.stepRegistry(t.Context(), run.ProjectID); err != nil || reg != nil {
		t.Fatalf("a copy's private Docker is logged in to a registry: %+v %v", reg, err)
	}
	if reg, err := f.exec.stepRegistry(t.Context(), idstest.N(999999)); err == nil || reg != nil {
		t.Fatalf("an unreadable project's registry: %+v %v", reg, err)
	}
	if env, err := f.exec.stepEnv(t.Context(), idstest.N(999999)); err == nil || env != nil {
		t.Fatalf("an unreadable project's step values: %v %v", env, err)
	}
}

// An eval copy's review step is given no instance step value either.
func TestACopysReviewIsGivenNoInstanceStepValue(t *testing.T) {
	t.Parallel()
	for _, copy := range []bool{false, true} {
		var env []string
		purpose := map[bool]project.Purpose{false: project.Ordinary, true: project.EvalCopy}[copy]
		_, _, _, run, _ := reviewFixtureFor(t, purpose, []schema.Step{{Name: "review", Uses: ReviewKind}}, func(req execution.Request) execution.Outcome {
			env = req.Env
			return reviewFile(t, req, `{"verdict":"pass","summary":"fine","findings":[]}`)
		}, func(e *Executor) {
			if err := os.WriteFile(filepath.Join(e.cfg.DataDir, "secrets.env"), []byte("STEP_CANARY="+stepSecretCanary+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		run("review")
		if carriesCanary(env) == copy {
			t.Fatalf("copy=%v: the review's environment %v", copy, env)
		}
	}
}

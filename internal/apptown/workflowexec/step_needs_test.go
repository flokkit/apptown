package workflowexec

import (
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/execution"
	"github.com/flokkit/apptown/internal/gitexec/gittest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flokkit/apptown/stock"
)

// The servers AppTown starts beside its own steps are where the app's own
// tests run, so they take the kind's step environment: the stock Postgres
// makes the app's user a superuser there, for test runners that make their
// own databases (Django) and load fixtures (Rails). An agent on a worker and
// the tests step both start it so; a deployment's and a preview's never do
// (dependency.TestOnlyAStepsPostgresMakesTheAppASuperuser).
func TestAgentsAndTestsStartAPostgresForTheirTests(t *testing.T) {
	f := agentOnWorkerFixture(t, map[string]any{"needs": true})
	// The fixture's postgres is a custom kind; the stock one replaces it.
	raw, err := stock.FS.ReadFile("dependencies/postgres.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.exec.dependencies.Save(t.Context(), string(raw), "tester"); err != nil {
		t.Fatal(err)
	}
	body := "app: {web: {image: nginx, public: true}}\nneeds: {postgres: {}}\n"
	f.call.commit = hubCommit(t, f.hub, map[string]string{".apptown/app.yaml": body})
	req := f.build(t)
	if p := req.spec.Private; p == nil || len(p.Services) != 1 || p.Services[0].Env["APP_SUPERUSER"] != "true" {
		t.Errorf("an agent on a worker: %+v", req.spec.Private)
	}

	staged := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staged, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, ".apptown/app.yaml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	services, _, _, _, err := (&commands{catalog: f.exec.dependencies}).testServices(t.Context(), staged)
	if err != nil || len(services) != 1 || services[0].Env["APP_SUPERUSER"] != "true" {
		t.Errorf("the tests step: %+v %v", services, err)
	}
}

// The local sandbox must describe the services it really starts to the agent
// and retain those names in its evidence, without connection values.
func TestLocalAgentNamesItsStartedNeed(t *testing.T) {
	t.Parallel()
	a, _, stepID := admittedAgentStep(t, `{"prompt":"test the app","needs":true}`)
	if _, _, err := a.agents.dependencies.SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.hub, ".apptown"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.hub, ".apptown/app.yaml"), []byte("app: {web: {image: nginx, public: true}}\nneeds: {postgres: {}}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, a.hub, "add", ".apptown/app.yaml")
	gittest.RunAsTester(t, a.hub, "commit", "-qm", "declare postgres")
	pi := installFakePi(a, func(_ int, req execution.Request) execution.Outcome {
		brief := req.Argv[len(req.Argv)-1]
		if !strings.Contains(brief, "- postgres: DATABASE_URL") {
			t.Errorf("local prompt lacks started need: %s", brief)
		}
		return execution.Outcome{Output: piSays("tested")}
	})
	if err := a.work.ExecuteStep(a.ctx, stepID); err != nil {
		t.Fatal(err)
	}
	status, evidence := stepStatus(t, a, stepID)
	if status != workflow.StatusPass || len(pi.requests()) != 1 {
		t.Fatalf("step: %s %v", status, evidence)
	}
	if !strings.Contains(fmt.Sprint(evidence["needs"]), "postgres") {
		t.Errorf("started need missing from evidence: %v", evidence)
	}
}

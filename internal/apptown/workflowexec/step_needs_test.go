package workflowexec

import (
	"os"
	"path/filepath"
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

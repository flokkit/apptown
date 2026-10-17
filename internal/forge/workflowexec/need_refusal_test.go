package workflowexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/stock"
)

// Agents on a worker, local agents and workflow tests fulfil an app's needs
// through the same catalogue refusal a release and a preview use: a
// declaration the contract withdrew (a password the forge sets, an
// integration under needs:) is refused, not started without it. (The
// fixture's postgres is a custom kind with no credentials; MariaDB is the
// stock one.)
func TestAgentsAndTestsRefuseTheNeedsAReleaseRefuses(t *testing.T) {
	for _, declaration := range []string{"{mariadb: {password: chosen}}", "{errors: {}}"} {
		t.Run(declaration, func(t *testing.T) {
			f := agentOnWorkerFixture(t, map[string]any{"needs": true})
			if _, _, err := f.exec.dependencies.SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
				t.Fatal(err)
			}
			body := "app: {web: {image: nginx, public: true}}\nneeds: " + declaration + "\n"
			f.call.commit = hubCommit(t, f.hub, map[string]string{".forge/app.yaml": body})
			_, err := f.exec.prepareAgentOnWorker(t.Context(), f.call)
			if err == nil {
				t.Error("worker agent accepted a withdrawn declaration")
			}

			staged := t.TempDir()
			if err := os.MkdirAll(filepath.Join(staged, ".forge"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(staged, ".forge/app.yaml"), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			commands := &Commands{catalog: f.exec.dependencies}
			_, _, unknown, _, err := commands.testServices(t.Context(), staged)
			if err == nil && len(unknown) == 0 {
				t.Error("workflow tests accepted a withdrawn declaration")
			}

			if strings.Contains(declaration, "errors") {
				_, _, cleanup, err := f.exec.startAgentSandbox(t.Context(), nil, nil, &agentConfig{Needs: true}, staged, nil, agentKnobs{})
				cleanup()
				if err == nil {
					t.Error("local agent accepted an integration as a need")
				}
			}
		})
	}
}

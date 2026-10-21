package workflowexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flokkit.io/forge/stock"
)

// Workflow tests fulfil an app's needs through the same catalogue refusal
// a release and a preview use: a declaration the contract withdrew (a
// password AppTown sets, an integration under needs:) is refused, not
// started without it. An agent asked to change the app is not refused: it
// may be the change that fixes the declaration, and refusing it would leave
// no governed way to (greeting-api#21). It starts no need services and its
// prompt says why. (The fixture's postgres is a custom kind with no
// credentials; MariaDB is the stock one.)
func TestAnAgentIsToldOfTheNeedsAReleaseRefusesAndTestsRefuseThem(t *testing.T) {
	for declaration, why := range map[string]string{
		"{mariadb: {password: chosen}}": "AppTown sets the password of the mariadb need",
		"{errors: {}}":                  "errors is an integration, not a need",
		"{mongodb: {}}":                 "this app needs mongodb, which is not in the dependency catalog",
	} {
		t.Run(declaration, func(t *testing.T) {
			f := agentOnWorkerFixture(t, map[string]any{"needs": true})
			if _, _, err := f.exec.dependencies.SyncStock(t.Context(), stock.FS, "dependencies"); err != nil {
				t.Fatal(err)
			}
			body := "app: {web: {image: nginx, public: true}}\nneeds: " + declaration + "\n"
			f.call.commit = hubCommit(t, f.hub, map[string]string{".apptown/app.yaml": body})
			req := f.build(t)
			if req.spec.Private != nil && len(req.spec.Private.Services) > 0 {
				t.Errorf("worker agent started need services: %+v", req.spec.Private.Services)
			}
			if prompt := req.spec.Argv[len(req.spec.Argv)-1]; !strings.Contains(prompt, "AppTown refuses this app's declared needs") || !strings.Contains(prompt, why) {
				t.Errorf("worker agent's prompt does not say why: %q", prompt)
			}

			staged := t.TempDir()
			if err := os.MkdirAll(filepath.Join(staged, ".apptown"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(staged, ".apptown/app.yaml"), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			commands := &Commands{catalog: f.exec.dependencies}
			_, _, unknown, _, err := commands.testServices(t.Context(), staged)
			if err == nil && len(unknown) == 0 {
				t.Error("workflow tests accepted a withdrawn declaration")
			}

			local := f.exec.workspaceNeedsRefusal(t.Context(), staged)
			if !strings.Contains(local, why) {
				t.Fatalf("local agent is told %q", local)
			}
			cfg := &agentConfig{Needs: true, needsRefused: local}
			_, _, cleanup, err := f.exec.startAgentSandbox(t.Context(), nil, nil, cfg, staged, nil, agentKnobs{})
			cleanup()
			if err != nil || len(cfg.Services) > 0 {
				t.Errorf("local agent: %v, services %+v", err, cfg.Services)
			}
		})
	}
}

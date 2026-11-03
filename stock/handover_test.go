package stock

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEveryBootingStageChecksConformanceAndTellsTheAgentTheBar(t *testing.T) {
	type step struct {
		Name, Uses string
		With       struct{ Prompt string }
	}
	for _, name := range []string{"quick-change", "dependency-uplift", "fix-bug", "plan-execute", "implement", "adopt-integration", "retire-integration"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(name + ".yaml")
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				Spec struct {
					Stages []struct {
						Name    string
						OnEnter []step `yaml:"onEnter"`
						Steps   []step
					}
				}
			}
			if err := yaml.Unmarshal(raw, &workflow); err != nil {
				t.Fatal(err)
			}
			for _, stage := range workflow.Spec.Stages {
				for _, s := range append(stage.OnEnter, stage.Steps...) {
					if s.Uses != "agent/pi" || !strings.Contains(s.With.Prompt, "Blockers:") {
						continue
					}
					prompt := strings.Join(strings.Fields(s.With.Prompt), " ")
					for _, rule := range []string{
						"A blocker is anything outside this repository that the change needs: a decision, a secret, access, a setting only a person can change (for example a stored preview secret), or a smaller request.",
						"Anything your report says a person or operator must do belongs in Blockers.",
						"Add the Blockers field only when there is at least one blocker. Leave it out otherwise.",
						"If you were sent back for such a reason, declare it as a blocker rather than trying again.",
						"Do not finish with a preview that fails to start unless the reason is listed in Blockers.",
					} {
						if !strings.Contains(prompt, rule) {
							t.Errorf("%s/%s lacks %s", stage.Name, s.Name, rule)
						}
					}
				}
				all := append(stage.OnEnter, stage.Steps...)
				boot, conformance, review, accept := -1, -1, -1, -1
				for i, s := range all {
					switch s.Uses {
					case "preview":
						boot = i
					case "guidance/conformance":
						conformance = i
					case "agent/review":
						review = i
					case "approval":
						accept = i
					}
				}
				if boot < 0 {
					continue
				}
				if conformance <= boot || review <= conformance || accept <= review {
					t.Fatalf("%s: boot=%d conformance=%d review=%d approval=%d", stage.Name, boot, conformance, review, accept)
				}
				for _, s := range all {
					if s.Uses != "agent/pi" {
						continue
					}
					for _, bar := range []string{"commit your changes", "apptown check", "apptown dev up", "what ran and what came back", "Blockers:", "declared `test:` commands", "Each command must work in a clean checkout: landing uses a fresh folder, so install dependencies first (`npm ci && npm test`)."} {
						if !strings.Contains(s.With.Prompt, bar) {
							t.Errorf("%s/%s lacks %s", stage.Name, s.Name, bar)
						}
					}
				}
			}
		})
	}
}

// Every place that teaches test declarations explains the fresh landing folder.
func TestDeclaredTestsExplainCleanCheckout(t *testing.T) {
	t.Parallel()
	paths := []string{
		"catalog.yaml",
		"guidance/frontend/vite-react.md",
		"guidance/lang/selection/django-fullstack.md",
		"guidance/lang/selection/dotnet-aspnet.md",
		"guidance/lang/selection/go-for-services.md",
		"guidance/lang/selection/java-spring-boot.md",
		"guidance/lang/selection/node-fullstack.md",
		"guidance/lang/selection/rails.md",
		"guidance/lang/selection/rust.md",
		"docs/guides/contract-reference.md",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(strings.Fields(string(raw)), " ")
			if !strings.Contains(text, "Each command must work in a clean checkout: landing uses a fresh folder, so install dependencies first (`npm ci && npm test`).") {
				t.Error("test declaration must explain the clean checkout and installing dependencies first")
			}
		})
	}
}

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
					for _, bar := range []string{"commit your changes", "apptown check", "apptown dev up", "what ran and what came back", "Blockers:"} {
						if !strings.Contains(s.With.Prompt, bar) {
							t.Errorf("%s/%s lacks %s", stage.Name, s.Name, bar)
						}
					}
				}
			}
		})
	}
}

package workflow

import (
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/ids"
	"strings"
	"testing"
)

func TestProjectLevelControlsCronBoundAndRecordedChoice(t *testing.T) {
	a, s, _, _, _ := standingFixture(t)
	s.deps.Portfolio = &standingPortfolioFixture{projectID: a.ProjectID}
	boss := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
	in := StandingDefinition{Scope: "instance", Categories: StandingCategories{Dependencies: "aggressive", SecurityFixes: true}, Delivery: "full"}
	rule := enableStanding(t, s, boss, in)
	if !strings.Contains(rule.Question.Shown, "Update level: aggressive") || !strings.Contains(rule.Question.Shown, "dependency updates, including major versions") {
		t.Fatal(rule.Question.Shown)
	}
	for _, level := range []string{"", "routine", "off", "aggressive"} {
		if level != "" {
			if err := s.SetProjectChoice(boss, a.ProjectID, level, "project decision"); err != nil {
				t.Fatal(err)
			}
		}
		got, cron, _, err := standingEligibilityOne(s, t.Context(), a.ProjectID, "test")
		if err != nil {
			t.Fatal(err)
		}
		if level == "off" {
			if security, _, _, err := standingEligibilityOne(s, t.Context(), a.ProjectID, "security"); err != nil || !security.RuleID.IsZero() {
				t.Fatalf("off security fired: %+v %v", security, err)
			}
			if !got.RuleID.IsZero() {
				t.Fatalf("off fired: %+v %s", got, cron)
			}
		} else {
			want := "aggressive"
			if level == "routine" {
				want = "routine"
			}
			if got.RuleID != rule.RuleID || cron != want {
				t.Fatalf("%s: %+v %s", level, got, cron)
			}
			_, security, _, err := standingEligibilityOne(s, t.Context(), a.ProjectID, "security")
			if err != nil || security != want {
				t.Fatalf("security cadence %s: %s %v", level, security, err)
			}
		}
		bound, err := s.UpdateBoundForRun(t.Context(), a.RunID)
		want := level
		if level == "" {
			want = "aggressive"
		}
		if level == "off" {
			want = ""
		}
		if err != nil || bound != want {
			t.Fatalf("bound %s: %s %v", level, bound, err)
		}
		if level != "" {
			state, err := s.ProjectState(boss, a.ProjectID)
			if err != nil || state.Level != level || state.Choice == nil || state.Choice.Person.Name != "boss" || state.Choice.At == "" || state.Choice.Reason != "project decision" {
				t.Fatalf("state %+v %v", state, err)
			}
		}
	}
	if err := s.SetProjectChoice(boss, a.ProjectID, "routine", "choose weekly updates"); err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview(boss, in)
	if err != nil || len(preview.Portfolio) != 1 || !preview.Portfolio[0].OverridesInstance || preview.Portfolio[0].Level != "routine" {
		t.Fatalf("preview missed override: %+v %v", preview, err)
	}
	in.Categories.Dependencies = "routine"
	preview, err = s.Preview(boss, in)
	if err != nil || !strings.Contains(preview.Shown, "Update level: routine") || !strings.Contains(preview.Shown, "patch and minor dependency updates") {
		t.Fatalf("routine preview %+v %v", preview, err)
	}

	for _, legacy := range []string{"patch", "minor"} {
		in.Categories.Dependencies = legacy
		if _, err := s.Preview(boss, in); err == nil {
			t.Fatalf("legacy bound %s accepted", legacy)
		}
	}
	// A preview identifies a project that differs from the instance level.
	if err := s.SetProjectChoice(boss, ids.Nil, "invalid", "invalid"); err == nil {
		t.Fatal("invalid level accepted")
	}
}

// Refusals and absent project choices belong to each workflow/rule judgment.
func TestStandingEligibilityKeepsMixedResultsAndEachRulesFallbackLevel(t *testing.T) {
	t.Parallel()
	a, s, p, _, _ := standingFixture(t)
	s.deps.Portfolio = &standingPortfolioFixture{projectID: a.ProjectID}
	boss := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
	in := StandingDefinition{Scope: "instance", Categories: StandingCategories{Dependencies: "routine", SecurityFixes: true}, Delivery: "full"}
	enableStanding(t, s, boss, in)
	p.means = "a newly approved purpose"
	p.docs = []*schema.Doc{{Metadata: schema.Metadata{Name: "test"}, Spec: schema.Spec{On: schema.On{Schedule: &schema.ScheduleTrigger{Standing: true, Cron: "0 6 * * 1", AggressiveCron: "0 6 * * *"}}}}}
	in.Categories.Dependencies = "aggressive"
	second := enableStanding(t, s, boss, in)
	results, err := s.StandingEligibility(t.Context(), []ProjectWorkflows{{ProjectID: a.ProjectID, Workflows: []string{"security", "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	refused, eligible := results[a.ProjectID]["security"], results[a.ProjectID]["test"]
	if refused.Err != nil || refused.Refused != "the standing approval meaning changed; a person must enable it again" || !refused.Rule.RuleID.IsZero() {
		t.Fatalf("refused workflow: %+v", refused)
	}
	if eligible.Err != nil || eligible.Refused != "" || eligible.Rule.RuleID != second.RuleID || eligible.Level != "aggressive" {
		t.Fatalf("eligible workflow: %+v", eligible)
	}
}

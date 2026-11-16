package workflow

import (
	"testing"
)

func TestProjectLevelControlsCronBoundAndRecordedChoice(t *testing.T) {
	a, s, _, _, _ := standingFixture(t)
	s.deps.Portfolio = &standingPortfolioFixture{projectID: a.ProjectID}
	boss := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
	in := JanitorChoices{Dependencies: "aggressive", SecurityFixes: true}
	rule := enableStanding(t, s, boss, in)

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
		bound, err := s.UpdateChoicesForRun(t.Context(), a.RunID)
		want := level
		if level == "" {
			want = "aggressive"
		}
		if level == "off" {
			want = ""
		}
		if err != nil || bound.Dependencies != want {
			t.Fatalf("bound %s: %+v %v", level, bound, err)
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

}

// Refusals and absent project choices belong to each workflow/rule judgment.

// Runtime meaning diagnostics respect project/workflow scope and unchanged adopters.

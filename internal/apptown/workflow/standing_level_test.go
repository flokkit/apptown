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

	for _, level := range []string{"", "routine", "off", "instance", "aggressive"} {
		if level != "" {
			if err := s.SetProjectChoice(boss, a.ProjectID, level); err != nil {
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
		if level == "" || level == "instance" {
			want = "aggressive"
		}
		if level == "off" {
			want = ""
		}
		if err != nil || bound.Dependencies != want {
			t.Fatalf("bound %s: %+v %v", level, bound, err)
		}
		if level != "" && level != "instance" {
			state, err := s.ProjectState(boss, a.ProjectID)
			if err != nil || state.Level != level || state.Choice == nil || state.Choice.Person.Name != "boss" || state.Choice.At == "" {
				t.Fatalf("state %+v %v", state, err)
			}
		}
	}
	// Returning to the instance choice removes the override from both owner reads.
	if err := s.SetProjectChoice(boss, a.ProjectID, "instance"); err != nil {
		t.Fatal(err)
	}
	for _, instance := range []string{"aggressive", "routine"} {
		rule = enableStanding(t, s, boss, JanitorChoices{Dependencies: instance})
		state, err := s.ProjectState(boss, a.ProjectID)
		if err != nil || state.Choice != nil || state.Level != instance || state.InstanceLevel != instance {
			t.Fatalf("inherited state: %+v %v", state, err)
		}
		got, level, refused, err := standingEligibilityOne(s, boss, a.ProjectID, "test")
		if err != nil || refused != "" || got.RuleID != rule.RuleID || level != instance {
			t.Fatalf("inherited eligibility: %+v %s %s %v", got, level, refused, err)
		}
	}
	if err := s.SetProjectChoice(boss, a.ProjectID, "routine"); err != nil {
		t.Fatal(err)
	}
	for _, instance := range []string{"aggressive", "routine", "off"} {
		enableStanding(t, s, boss, JanitorChoices{Dependencies: instance})
		state, err := s.ProjectState(boss, a.ProjectID)
		want := "routine"
		if instance == "off" {
			want = "off"
		}
		if err != nil || state.Choice == nil || state.Choice.Level != "routine" || state.Level != want || state.InstanceLevel != instance {
			t.Fatalf("pinned state: %+v %v", state, err)
		}
	}
}

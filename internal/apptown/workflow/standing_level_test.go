package workflow

import (
	"github.com/flokkit/apptown/internal/ids"
	"strings"
	"testing"
)

func TestProjectLevelControlsCronBoundAndRecordedChoice(t *testing.T) {
	a, s, _, _, _ := standingFixture(t)
	s.deps.Portfolio = &standingPortfolioFixture{projectID: a.ProjectID}
	boss := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
	in := StandingDefinition{Scope: "instance", Schedules: []standingSchedule{
		{Workflow: "test", Crons: map[string]string{"routine": "0 6 * * 1", "aggressive": "0 6 * * *"}},
		{Workflow: "security", Crons: map[string]string{"routine": "0 5 * * *", "aggressive": "0 5 * * *"}},
	}, Categories: StandingCategories{Dependencies: "aggressive", SecurityFixes: true}, Delivery: "full"}
	rule := enableStanding(t, s, boss, in)
	if !strings.Contains(rule.Question.Shown, "Update level: aggressive") || !strings.Contains(rule.Question.Shown, "every night applies dependency updates, including major versions") {
		t.Fatal(rule.Question.Shown)
	}
	for _, level := range []string{"", "routine", "off", "aggressive"} {
		if level != "" {
			if err := s.SetProjectChoice(boss, a.ProjectID, level, "project decision"); err != nil {
				t.Fatal(err)
			}
		}
		got, cron, err := s.StandingForWorkflow(t.Context(), a.ProjectID, "test")
		if err != nil {
			t.Fatal(err)
		}
		if level == "off" {
			if security, _, err := s.StandingForWorkflow(t.Context(), a.ProjectID, "security"); err != nil || !security.RuleID.IsZero() {
				t.Fatalf("off security fired: %+v %v", security, err)
			}
			if !got.RuleID.IsZero() || cron != "" {
				t.Fatalf("off fired: %+v %s", got, cron)
			}
		} else {
			want := "0 6 * * *"
			if level == "routine" {
				want = "0 6 * * 1"
			}
			if got.RuleID != rule.RuleID || cron != want {
				t.Fatalf("%s: %+v %s", level, got, cron)
			}
			_, security, err := s.StandingForWorkflow(t.Context(), a.ProjectID, "security")
			if err != nil || security != "0 5 * * *" {
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
	if err != nil || !strings.Contains(preview.Shown, "Update level: routine") || !strings.Contains(preview.Shown, "each Monday applies patch and minor dependency updates") {
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

package workflow

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
	"slices"
	"strings"
	"testing"
	"time"
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
	for _, staleFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(staleFirst), func(t *testing.T) {
			a, s, p, _, _ := standingFixture(t)
			s.deps.Portfolio = &standingPortfolioFixture{projectID: a.ProjectID}
			boss := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
			in := StandingDefinition{Scope: "instance", Categories: StandingCategories{Dependencies: "routine", SecurityFixes: true}, Delivery: "full"}
			var stale, winner StandingRevision
			enableStale := func() { p.means = "old purpose"; p.docs = nil; stale = enableStanding(t, s, boss, in) }
			enableWinner := func() {
				p.means = "current purpose"
				p.docs = []*schema.Doc{{Metadata: schema.Metadata{Name: "test"}, Spec: schema.Spec{On: schema.On{Schedule: &schema.ScheduleTrigger{Standing: true, Cron: "0 6 * * 1", AggressiveCron: "0 6 * * *"}}}}}
				in.Categories.Dependencies = "aggressive"
				winner = enableStanding(t, s, boss, in)
				in.Categories.Dependencies = "routine"
			}
			if staleFirst {
				enableStale()
				enableWinner()
			} else {
				enableWinner()
				enableStale()
			}
			p.means = "current purpose"
			results, err := s.StandingEligibility(t.Context(), []ProjectWorkflows{{ProjectID: a.ProjectID, Workflows: []string{"security", "test"}}})
			if err != nil {
				t.Fatal(err)
			}
			refused := results[a.ProjectID]["security"]
			if refused.Err != nil || refused.Refused != errStandingMeaningChanged.Error() || !refused.Rule.RuleID.IsZero() || !slices.Equal(refused.MeaningChanged, []ids.ID{stale.RuleID}) {
				t.Fatalf("refused workflow: %+v", refused)
			}
			got := results[a.ProjectID]["test"]
			if got.Err != nil || got.Refused != "" || got.Rule.RuleID != winner.RuleID || got.Level != "aggressive" || !slices.Equal(got.MeaningChanged, []ids.ID{stale.RuleID}) {
				t.Fatalf("fallback: %+v", got)
			}
		})
	}
}

// Runtime meaning diagnostics respect project/workflow scope and unchanged adopters.
func TestStandingMeaningChangeRespectsProjectScope(t *testing.T) {
	for _, instance := range []bool{true, false} {
		t.Run(fmt.Sprint(instance), func(t *testing.T) {
			a, rules, p, owner, in := standingFixture(t)
			rules.deps.Portfolio = &standingPortfolioFixture{projectID: a.ProjectID}
			second := workflowTestProject(t, a.DB, "unchanged", "Unchanged", "")
			ctx := owner
			if instance {
				in = StandingDefinition{Scope: "instance", Categories: StandingCategories{Dependencies: "routine"}, Delivery: "full"}
				ctx = stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, a.Access, "boss"))
			}
			name := "quick-change"
			if instance {
				name = "test"
			}
			rule := enableStanding(t, rules, ctx, in)
			p.projectMeans = map[ids.ID]string{a.ProjectID: "changed meaning"}
			_, err := rules.eligibleForIn(t.Context(), a.DB, rule, a.ProjectID, name)
			if !errors.Is(err, errStandingMeaningChanged) {
				t.Fatalf("typed refusal: %v", err)
			}
			results, err := rules.StandingEligibility(t.Context(), []ProjectWorkflows{{ProjectID: a.ProjectID, Workflows: []string{name}}, {ProjectID: second, Workflows: []string{name}}})
			if err != nil {
				t.Fatal(err)
			}
			changed, unchanged := results[a.ProjectID][name], results[second][name]
			if !slices.Equal(changed.MeaningChanged, []ids.ID{rule.RuleID}) || !changed.Rule.RuleID.IsZero() {
				t.Fatalf("changed: %+v", changed)
			}
			if len(unchanged.MeaningChanged) != 0 || instance && unchanged.Rule.RuleID != rule.RuleID {
				t.Fatalf("unchanged: %+v", unchanged)
			}
		})
	}
}

func TestJanitorReadsChangedRulesWithoutOverridingEligibleFallback(t *testing.T) {
	for _, scope := range []string{"instance", "project"} {
		for _, fallback := range []string{"none", "before", "after"} {
			t.Run(scope+"/"+fallback, func(t *testing.T) {
				service, rules, ctx, _ := janitorReadFixture(t)
				visible, err := service.authority.VisibleProjects(ctx, actionReadProject)
				if err != nil {
					t.Fatal(err)
				}
				pid := visible[0]
				second := workflowTestProject(t, service.db, "unchanged", "Unchanged", "")
				receipt, err := adoption.CurrentReceiptIn(ctx, service.db, pid)
				if err != nil {
					t.Fatal(err)
				}
				if err := service.db.InTx(ctx, func(tx *database.Tx) error {
					return adoption.RecordIn(ctx, tx, second, 1, adoption.AdoptionCreate, "fixture", receipt.Receipt, "boss", time.Now())
				}); err != nil {
					t.Fatal(err)
				}
				service.authority = aggregateAuthority{operationAuthority: service.authority, read: func(context.Context, commandAction) ([]ids.ID, error) { return []ids.ID{pid, second}, nil }}

				in := StandingDefinition{Scope: "instance", Categories: StandingCategories{Dependencies: "routine"}, Delivery: "full"}
				if scope == "project" {
					in = StandingDefinition{Projects: []StandingProject{{ProjectID: pid, Workflow: "janitor-update"}}, Categories: StandingCategories{Dependencies: "routine"}, Delivery: "land-only"}
				}
				p := rules.deps.Procedures.(*standingProcedureFixture)
				rules.deps.Portfolio = &standingPortfolioFixture{projectID: pid}
				enableFresh := func() {
					p.means = "current purpose"
					in.Categories.Dependencies = "aggressive"
					enableStanding(t, *rules, ctx, in)
					in.Categories.Dependencies = "routine"
				}
				if fallback == "before" {
					enableFresh()
				}
				p.means = "old purpose"
				stale := enableStanding(t, *rules, ctx, in)
				if fallback == "after" {
					enableFresh()
				}
				p.means = "current purpose"
				p.projectMeans = map[ids.ID]string{second: "old purpose"}
				if fallback != "none" {
					if err := rules.SetProjectChoice(ctx, second, "off", "fixture"); err != nil {
						t.Fatal(err)
					}
				}
				rows, _, changedIDs, _, err := service.JanitorProjects(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 2 {
					t.Fatalf("rows: %+v", rows)
				}
				var affected, unchanged JanitorProject
				for _, row := range rows {
					if row.ProjectID == pid {
						affected = row
					} else {
						unchanged = row
					}
				}
				if unchanged.State.Word == "needs you" || len(unchanged.Eligibility["janitor-update"].MeaningChanged) != 0 {
					t.Fatalf("unaffected project: %+v", unchanged)
				}
				if !slices.Contains(changedIDs, stale.RuleID) {
					t.Fatalf("changed rule missing: %+v", changedIDs)
				}
				if (affected.State.Word == "needs you") != (fallback == "none") {
					t.Fatalf("fallback %s: %+v", fallback, affected.State)
				}
				changed, err := service.ChangedStandingApprovals(ctx)
				if err != nil || len(changed) != 1 || changed[0].RuleID != stale.RuleID {
					t.Fatalf("changed read: %+v %v", changed, err)
				}
				p.means = "old purpose"
				if fallback == "none" {
					changed, err = service.ChangedStandingApprovals(ctx)
					if err != nil || len(changed) != 0 {
						t.Fatalf("unchanged read: %+v %v", changed, err)
					}
				}
			})
		}
	}
}

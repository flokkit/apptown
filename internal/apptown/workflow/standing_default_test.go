package workflow

import (
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/database"
)

// The first revision is durable installation authority; any saved history wins.
func TestDefaultJanitorChoicesSeedsOnceAndPreservesExistingRecords(t *testing.T) {
	for _, mode := range []string{"empty", "saved", "all off", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			a, rules, _, ctx, choices := standingFixture(t)
			switch mode {
			case "saved", "disabled":
				enableStanding(t, rules, ctx, choices)
			case "all off":
				enableStanding(t, rules, ctx, JanitorChoices{Dependencies: "off"})
			}
			if mode == "disabled" {
				if err := StartJanitorChoices(ctx, a.DB, time.Now(), true); err != nil {
					t.Fatal(err)
				}
			}
			before, err := rules.History(ctx)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC().Truncate(time.Microsecond)
			for range 2 {
				if err := StartJanitorChoices(ctx, a.DB, at, false); err != nil {
					t.Fatal(err)
				}
			}
			head, err := rules.Current(ctx)
			if err != nil || head == nil {
				t.Fatalf("head: %+v %v", head, err)
			}
			history, err := rules.History(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "empty" {
				if !reflect.DeepEqual(before, history) || *head != history[len(history)-1] {
					t.Fatalf("existing choices changed: before=%+v after=%+v head=%+v", before, history, head)
				}
				return
			}
			if len(history) != 1 || *head != history[0] || head.RuleID.IsZero() || head.Revision != 1 || !head.Enabled || head.Choices != (JanitorChoices{Dependencies: "routine", SecurityFixes: true, BaseImages: true, ToolchainPatches: true, Packs: true}) || head.Issuer != appTownIssuer || head.Issuer.Principal.IsZero() || head.Issuer.Via != access.SystemInstrument || !head.At.Equal(at) {
				t.Fatalf("default revision: %+v history=%+v", head, history)
			}
			caller, _ := access.CallerFrom(ctx)
			saved := enableStanding(t, rules, ctx, JanitorChoices{Dependencies: "off"})
			if saved.Revision != 2 || saved.RuleID != head.RuleID || saved.Issuer != caller.Principal.Actor() || saved.Enabled {
				t.Fatalf("person's replacement: %+v", saved)
			}
		})
	}
}

func TestDefaultJanitorChoicesPermitsSchedulesAndPacksUntilProjectOptsOut(t *testing.T) {
	t.Parallel()
	a, rules, _, ctx, _ := standingFixture(t)
	if err := StartJanitorChoices(ctx, a.DB, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	results, err := rules.StandingEligibility(ctx, []ProjectWorkflows{{ProjectID: a.ProjectID, Workflows: []string{"nightly"}}})
	got := results[a.ProjectID]["nightly"]
	if err != nil || got.Err != nil || got.Refused != "" || got.Rule.Issuer != appTownIssuer || got.Level != "routine" {
		t.Fatalf("eligibility: %+v %v", got, err)
	}
	rule, err := rules.PackRuleIn(ctx, a.DB)
	if err != nil || rule.Issuer != appTownIssuer {
		t.Fatalf("pack rule: %+v %v", rule, err)
	}
	if err := a.DB.InTx(ctx, func(tx *database.Tx) error { return rules.PackReviewAuthorityIn(tx, rule).RequirePackReview(ctx) }); err != nil {
		t.Fatalf("pack review authority: %v", err)
	}
	if err := rules.SetProjectChoice(ctx, a.ProjectID, "off"); err != nil {
		t.Fatal(err)
	}
	results, err = rules.StandingEligibility(ctx, []ProjectWorkflows{{ProjectID: a.ProjectID, Workflows: []string{"nightly"}}})
	got = results[a.ProjectID]["nightly"]
	if err != nil || got.Err != nil || got.Refused != "turned off for this project" {
		t.Fatalf("project off: %+v %v", got, err)
	}
}

func TestRestoreDisablesSeededChoicesAndStartupDoesNotReenableThem(t *testing.T) {
	t.Parallel()
	a, rules, _, ctx, _ := standingFixture(t)
	if err := StartJanitorChoices(ctx, a.DB, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if err := StartJanitorChoices(ctx, a.DB, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if err := StartJanitorChoices(ctx, a.DB, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	head, err := rules.Current(ctx)
	if err != nil || head == nil || head.Enabled || head.Revision != 2 || head.Choices.enabled() {
		t.Fatalf("restored head: %+v %v", head, err)
	}
}

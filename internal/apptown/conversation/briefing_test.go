package conversation

import (
	"strings"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// The project console is the FRONT DESK; a change console is the WORKSHOP.
// Both prompts are rendered from the same sections — the front-desk-only
// ones (routing advice, invoke/apply instructions, the read-only frame)
// are dropped for a workshop, and the workshop brings its own identity.
// This pins the selection so an edit to a section cannot leak front-desk
// framing into a workshop, or leave the read-only frame on a writable
// session.
func TestConsoleSteeringFrames(t *testing.T) {
	t.Parallel()
	db := databaseSuite.New(t)
	ways := wow.New(db, wow.Dependencies{})
	steering := NewBriefing(ways, adoption.New(db, adoption.Dependencies{Ways: ways}), BriefingConfig{FrontDeskVerbs: []string{"`apptown invoke`", "`apptown apply`"}, WorkshopVerbs: []string{"`apptown preview`", "`apptown land`"}})
	p := &project.Project{ID: idstest.N(1), Slug: "p", Name: "P"}

	desk := steering.Console(t.Context(), p, "")
	for _, want := range []string{"front desk", "apptown apply", "apptown invoke", "## Starting work", "strictly read-only",
		"The only mutations your credentials permit"} {
		if !strings.Contains(desk, want) {
			t.Errorf("front-desk steering must contain %q", want)
		}
	}
	for _, banned := range []string{"WORKSHOP", "WRITABLE", "apptown preview", "apptown land"} {
		if strings.Contains(desk, banned) {
			t.Errorf("front-desk steering must not contain %q — a workshop-only fact leaked", banned)
		}
	}

	shop := steering.Workshop(t.Context(), p, "")
	for _, banned := range []string{"front desk", "FRONT DESK", "## Starting work", "strictly read-only",
		"You cannot edit code", "apptown invoke -w"} {
		if strings.Contains(shop, banned) {
			t.Errorf("workshop steering must not contain %q — a front-desk-only section leaked", banned)
		}
	}
	for _, want := range []string{"WORKSHOP", "WRITABLE", "apptown invoke/apply are DISABLED", "apptown preview", "apptown land"} {
		if !strings.Contains(shop, want) {
			t.Errorf("workshop steering must contain %q", want)
		}
	}
	// Every section the workshop keeps is in the front desk too: the
	// workshop is a selection, never a second prompt.
	for _, name := range []string{"## Your environment", "## apptown CLI (read commands)", "## Ground rules"} {
		if !strings.Contains(desk, name) || !strings.Contains(shop, name) {
			t.Errorf("shared section %q must appear in both prompts", name)
		}
	}
}

// A change chat's helper reports a request it did not finish as unfinished:
// the walkthrough's helper changed the tab title when a visible heading was
// asked for, and said "done… verified".
func TestChangeBriefingAsksForShortfallsFirst(t *testing.T) {
	t.Parallel()
	steering := NewBriefing(nil, nil, BriefingConfig{DataDir: t.TempDir()})
	text := steering.Change(&workflow.Change{ID: idstest.N(7), Title: "heading", Branch: "change-7"})
	for _, want := range []string{"measure the work against what was ASKED", "say that FIRST", `Never say
  "done" or "verified"`} {
		if !strings.Contains(text, want) {
			t.Errorf("change briefing must contain %q", want)
		}
	}
}

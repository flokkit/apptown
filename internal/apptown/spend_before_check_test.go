package apptown

import (
	"context"
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/ids"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

// viewerAndContributor adds to the authz fixture's project "game" (1) a
// person who may only look (vic, project viewer) and one who may work on
// changes (jenny, project contributor).
func viewerAndContributor(t *testing.T, a *App) {
	t.Helper()
	for who, role := range map[string]string{"vic": access.RoleProjectViewer, "jenny": access.RoleProjectContrib} {
		if _, err := a.Access.EnsureUser(context.Background(), who); err != nil {
			t.Fatal(err)
		}
		if a.addGrant("user", who, role, "", access.ScopeProject, idstest.N(1).String(), "fixture", "boss", "").IsZero() {
			t.Fatalf("granting %s %s", who, role)
		}
	}
}

// Refreshing a change's narrative spends a model call before it publishes.
// The source read asks for the publication's authority, so a person who
// may only read the change is refused before the model is called.
func TestNarrativeRefreshRefusesBeforeSpending(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	viewerAndContributor(t, a)
	calls := 0
	b := narrativeBuilder{work: a.Work,
		resolve: func(context.Context, ids.ID) (string, string) { return "openrouter/fixture", "fast" },
		evidence: func(context.Context, workflow.NarrativeSource) (narrativeEvidence, error) {
			return narrativeEvidence{Text: "DIFF: one line"}, nil
		},
		models: askFunc(func(context.Context, modelcall.Call) (string, *modelcall.Usage, error) {
			calls++
			return "TITLE: Blue cat\nASKED: yes\n\nThe cat is blue now.", &modelcall.Usage{}, nil
		}),
	}
	err := b.Refresh(asPerson("vic"), fixtureWork(t, a).Change)
	var denied *access.Denied
	if !errors.As(err, &denied) || calls != 0 {
		t.Fatalf("a viewer's refresh: %v, %d model call(s)", err, calls)
	}
	if err := b.Refresh(asPerson("jenny"), fixtureWork(t, a).Change); err != nil || calls != 1 {
		t.Fatalf("a contributor's refresh: %v, %d model call(s)", err, calls)
	}
	ch, err := a.Work.Change(asPerson("vic"), fixtureWork(t, a).Change)
	if err != nil || ch.Summary != "The cat is blue now." {
		t.Fatalf("published narrative: %q %v", ch.Summary, err)
	}
}

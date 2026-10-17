package forge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/forge/modelcall"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/ids"
	"flokkit.io/forge/internal/ids/idstest"
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

// The one-box change may spend on a model call to choose its title and
// lane. A person who may not open and start the change is refused by that
// authority before the choosing, not by the creation after it.
func TestSimpleChangeRefusesAViewerBeforeChoosing(t *testing.T) {
	t.Parallel()
	a := authzFixture(t)
	viewerAndContributor(t, a)
	h := a.Handler()
	w := authzCall(h, "vic", "POST", "/api/v1/projects/game/changes/simple", `{"text":"make the cat blue"}`)
	if w.Code != 403 || w.Header().Get("X-Forge-Deny-Code") == "" || !strings.Contains(w.Body.String(), "vic may not") {
		t.Fatalf("a viewer's one-box change must be refused by authority: %d %s", w.Code, w.Body.String())
	}
	changes, err := a.Work.Changes(asPerson("owner"), idstest.N(1), workflow.ChangeList{})
	if err != nil || len(changes) != 1 {
		t.Fatalf("a refused one-box change opened a change: %+v %v", changes, err)
	}
	// Someone who may: past authority, to the project's lanes (none simple).
	if w := authzCall(h, "jenny", "POST", "/api/v1/projects/game/changes/simple", `{"text":"make the cat blue"}`); w.Code != 422 {
		t.Fatalf("a contributor's one-box change reaches the lane choice: %d %s", w.Code, w.Body.String())
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

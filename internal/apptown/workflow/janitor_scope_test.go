package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
)

func TestJanitorAttributionDoesNotExposeUnreadableProject(t *testing.T) {
	t.Parallel()
	s, rules, admin, _ := janitorReadFixture(t)
	visible, err := s.authority.VisibleProjects(admin, actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	pid := visible[0]
	hidden := workflowTestProject(t, s.db, "confidential-project", "Confidential acquisition", "")
	rule := enableStanding(t, *rules, admin, StandingDefinition{
		Administrative: true,
		Projects:       []StandingProject{{ProjectID: pid, Workflow: "janitor-update"}, {ProjectID: hidden, Workflow: "janitor-update"}},
		Categories:     StandingCategories{Dependencies: "routine"}, Delivery: "land-only",
	})
	ch := janitorTry(t, s, pid, rule, time.Now().UTC(), false)
	directory := rules.deps.Identity
	if _, err := directory.EnsurePerson(t.Context(), "limited-reader"); err != nil {
		t.Fatal(err)
	}
	for _, capability := range []access.Capability{access.CapProjectRead, access.CapChangeRead} {
		if _, err := directory.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "limited-reader", Capability: string(capability), Scope: access.ScopeProject, ScopeID: pid.String(), By: "boss"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	reader := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, directory, "limited-reader"))
	if _, err := rules.History(reader, rule.RuleID); err == nil {
		t.Fatal("fixture reader can read the cross-project grant")
	}
	projects := project.New(s.db, identity.Scoped(directory, "project"))
	checked := access.Checked{Authority: directory, Projects: projects}
	resource, err := project.AuthorityResourceIn(reader, s.db, hidden)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checked.Require(reader, access.Permission{Capability: access.CapProjectRead, Resource: resource}); err == nil {
		t.Fatal("fixture reader can read the hidden project")
	}
	read, err := NewChanges(s.db, checked, nil).Change(reader, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Janitor == nil || read.Janitor.Rule == nil {
		t.Fatal("missing visible Change attribution")
	}
	if read.Janitor.Rule.Scope != "Scan project and 1 other project" {
		t.Fatalf("visible Change exposes unreadable project: %q", read.Janitor.Rule.Scope)
	}
}

// Both fully readable and entirely hidden scopes keep a useful label.
func TestJanitorAttributionScopeCountsHiddenProjects(t *testing.T) {
	t.Parallel()
	s, rules, admin, _ := janitorReadFixture(t)
	visible, err := s.authority.VisibleProjects(admin, actionReadProject)
	if err != nil {
		t.Fatal(err)
	}
	pid := visible[0]
	hidden := workflowTestProject(t, s.db, "confidential-project", "Confidential acquisition", "")
	another := workflowTestProject(t, s.db, "another-project", "Another confidential project", "")
	directory := rules.deps.Identity
	if _, err := directory.EnsurePerson(t.Context(), "limited-reader"); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: "limited-reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: pid.String(), By: "boss"}, ""); err != nil {
		t.Fatal(err)
	}
	reader := stepVerifiedContext(t.Context(), testAdmittedPrincipal(t, directory, "limited-reader"))
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		projects []StandingProject
		want     string
	}{
		{"readable", reader, []StandingProject{{ProjectID: pid}}, "Scan project"},
		{"hidden", reader, []StandingProject{{ProjectID: hidden}}, "1 other project"},
		{"all hidden", reader, []StandingProject{{ProjectID: hidden}, {ProjectID: another}}, "2 other projects"},
		{"some hidden", reader, []StandingProject{{ProjectID: pid}, {ProjectID: hidden}, {ProjectID: another}}, "Scan project and 2 other projects"},
		{"administrator", admin, []StandingProject{{ProjectID: pid}, {ProjectID: hidden}}, "Scan project, Confidential acquisition"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := StandingRevision{Question: StandingQuestion{Definition: StandingDefinition{Administrative: true, Projects: tc.projects}}}
			got, err := standingRuleIdentityIn(tc.ctx, s.db, s.checked, rule)
			if err != nil {
				t.Fatal(err)
			}
			if got.Scope != tc.want {
				t.Fatalf("scope = %q, want %q", got.Scope, tc.want)
			}
		})
	}
}

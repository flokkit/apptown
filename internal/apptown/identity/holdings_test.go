package identity

import (
	"testing"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/dbtest"
	"github.com/flokkit/apptown/internal/ids/idstest"
)

func TestHoldingsUsesCurrentFactsAndDelegationAndFailsUnreadableFacts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	s := New(db, Config{Projects: project.NewCatalogue(db)})
	if err := s.SeedAdministrator(t.Context(), "boss"); err != nil {
		t.Fatal(err)
	}
	person := admittedPrincipal(t, s, "boss")
	resource := access.Resource{Type: "project", ID: idstest.N(1).String(), ProjectID: idstest.N(1)}
	for _, pr := range []access.Principal{person, {ID: person.ID, Kind: "instrument", Name: "console/boss", Instrument: "console", Person: "boss", ProjectID: resource.ProjectID}} {
		held, err := s.Holdings(t.Context(), pr, resource)
		if err != nil || len(held) != len(access.Definitions) {
			t.Fatalf("holdings: %v %v", held, err)
		}
		for capability := range access.Definitions {
			decision, err := s.Explain(t.Context(), pr, access.Permission{Capability: capability, Resource: resource})
			if err != nil || held[capability] != decision.Allow {
				t.Errorf("%s differs from checked decision: %v", capability, err)
			}
		}
		if pr.Kind == "instrument" && held[access.CapChangeApprove] {
			t.Fatal("console inherited a person's approval authority")
		}
	}
	for _, marker := range []string{"-- name: UserByPrincipal :one", "-- name: ActiveGrants"} {
		clear := dbtest.NextStatementFailure(db, marker)
		got, err := s.Holdings(t.Context(), person, resource)
		clear()
		if err == nil || got != nil {
			t.Fatalf("lost %s failure: %v %v", marker, got, err)
		}
	}
	if _, err := s.EnsurePerson(t.Context(), "reader"); err != nil {
		t.Fatal(err)
	}
	reader := admittedPrincipal(t, s, "reader")
	if _, err := s.Grant(t.Context(), Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeInstance}, ""); err != nil {
		t.Fatal(err)
	}
	before, err := s.Holdings(t.Context(), reader, resource)
	if err != nil || !before[access.CapProjectRead] {
		t.Fatalf("reader grant: %v %v", before, err)
	}
	disabled := true
	if err := s.SaveUser(t.Context(), AccountChange{Username: "reader", Disabled: &disabled}, person); err != nil {
		t.Fatal(err)
	}
	held, err := s.Holdings(t.Context(), reader, resource)
	if err != nil {
		t.Fatal(err)
	}
	for capability, allow := range held {
		if allow {
			t.Errorf("disabled person holds %s", capability)
		}
	}
}

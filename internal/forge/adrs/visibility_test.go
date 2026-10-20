package adrs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/dbtest"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/ids"
)

func TestAggregateVisibilityPrecedesLimitsAndCallerFilters(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	accounts := identity.New(db, identity.Config{Projects: project.NewCatalogue(db)})
	if err := accounts.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"reader", "outsider"} {
		if _, err := accounts.EnsureUser(context.Background(), who); err != nil {
			t.Fatal(err)
		}
	}
	public := projectFixture(t, db, "z-visible", "Visible")
	foreign := projectFixture(t, db, "a-private", "Private")
	if _, err := accounts.Grant(context.Background(), identity.Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: fmt.Sprint(public)}, ""); err != nil {
		t.Fatal(err)
	}
	s := New(db, access.Checked{Authority: accounts, Projects: project.New(db, accounts)}, nil, nil)
	boss := adrAs(access.Principal{Kind: "user", Name: "boss"})
	reader := adrAs(access.Principal{Kind: "user", Name: "reader"})
	visible, err := s.Create(boss, Create{ProjectID: public, Title: "visible decision", BodyMD: "visible prose", Tags: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := s.Create(boss, Create{ProjectID: foreign, Title: fmt.Sprintf("foreign %d", i), BodyMD: "hidden prose", Tags: "shared"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []Filter{{Limit: 1}, {Limit: 1, Recent: true}, {Tag: "shared", Limit: 1}} {
		rows, err := s.List(reader, f)
		if err != nil || len(rows) != 1 || rows[0].ID != visible.ID {
			t.Fatalf("filter before limit: %+v %v", rows, err)
		}
	}
	for _, f := range []Filter{{ProjectSlug: "a-private"}, {Query: "hidden"}} {
		rows, err := s.List(reader, f)
		if err != nil || len(rows) != 0 {
			t.Fatalf("foreign filter: %+v %v", rows, err)
		}
	}
	if _, err := s.List(reader, Filter{ProjectID: foreign}); err == nil {
		t.Fatal("explicit foreign project admitted")
	}
	counts, err := s.Counts(reader, []ids.ID{foreign, public})
	if err != nil || len(counts) != 1 || counts[public] != 1 {
		t.Fatalf("apptownd count IDs: %+v %v", counts, err)
	}
	for _, idList := range [][]ids.ID{nil, {}, {foreign}} {
		counts, err := s.Counts(reader, idList)
		if err != nil || len(counts) != 0 {
			t.Fatalf("empty/foreign counts: %v %v", counts, err)
		}
	}
	for _, idList := range [][]ids.ID{nil, {}} {
		rows, err := readRecords(context.Background(), db, Filter{}, idList)
		if err != nil || rows == nil || len(rows) != 0 {
			t.Fatalf("nil/empty SQL fence: %+v %v", rows, err)
		}
	}
	rows, err := s.List(adrAs(access.Principal{Kind: "user", Name: "outsider"}), Filter{})
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("outsider aggregate: %+v %v", rows, err)
	}
	if _, err := s.List(context.Background(), Filter{}); !errors.Is(err, ErrCallerRequired) {
		t.Fatalf("unverified aggregate: %v", err)
	}
	dbtest.FailNextStatement(t, db, "-- name: ProjectAuthorityCatalogue")
	if rows, err := s.List(reader, Filter{}); err == nil || rows != nil {
		t.Fatalf("catalogue failure narrowed silently: %+v %v", rows, err)
	}
}

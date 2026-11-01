package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/dbtest"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

func TestVisibleProjectsUsesCurrentCheckedAuthorityWithoutReceipts(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	catalogue := project.NewCatalogue(db)
	s := New(db, Config{Projects: catalogue})
	if err := s.SeedAdministrator(context.Background(), "boss"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reader", "group-reader", "outsider"} {
		if _, err := s.EnsureUser(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveGroup(context.Background(), GroupChange{Name: "family", Members: []string{"group-reader"}}, admittedPrincipal(t, s, "boss")); err != nil {
		t.Fatal(err)
	}
	projectIDs := []ids.ID{}
	for i := range 3 {
		id, err := database.Transact(context.Background(), db, func(tx *database.Tx) (ids.ID, error) {
			return project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: fmt.Sprintf("p%d", i), OwnerGroup: []string{"family", "elsewhere", "family"}[i]})
		})
		if err != nil {
			t.Fatal(err)
		}
		projectIDs = append(projectIDs, id)
	}
	grant, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: fmt.Sprint(projectIDs[0])}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Grant(context.Background(), Spec{SubjectType: "group", Subject: "family", Capability: string(access.CapProjectRead), Scope: access.ScopeGroup, ScopeID: "family"}, ""); err != nil {
		t.Fatal(err)
	}
	read := func(pr access.Principal, want []ids.ID) {
		t.Helper()
		got, err := s.VisibleProjects(context.Background(), pr, access.CapProjectRead)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("%+v: got %v, %v; want %v", pr, got, err, want)
		}
		// Compare with checked project decisions, within the independently stated
		// candidate bound. Expected IDs above also protect against oracle agreement.
		resources, err := catalogue.ProjectResources(context.Background(), pr.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		oracle := []ids.ID{}
		for _, r := range resources {
			d, err := s.Explain(context.Background(), pr, access.Permission{Capability: access.CapProjectRead, Resource: r})
			if err != nil {
				t.Fatal(err)
			}
			if d.Allow {
				oracle = append(oracle, r.ProjectID)
			}
		}
		if !slices.Equal(got, oracle) {
			t.Fatalf("oracle: %v != %v", got, oracle)
		}
	}
	checkCases := func() {
		read(admittedPrincipal(t, s, "boss"), projectIDs)
		read(admittedPrincipal(t, s, "reader"), projectIDs[:1])
		read(admittedPrincipal(t, s, "group-reader"), []ids.ID{projectIDs[0], projectIDs[2]})
		read(admittedPrincipal(t, s, "outsider"), nil)
		read(access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", ProjectID: projectIDs[0]}, projectIDs[:1])
		read(access.Principal{Kind: "instrument", Name: "console/1", Instrument: "console", Person: "boss", ProjectID: projectIDs[1]}, projectIDs[1:2])
		read(access.Principal{Kind: "instrument", Name: "engine", Instrument: access.SystemInstrument}, projectIDs)
		read(access.Principal{Kind: "instrument", Name: "trigger", Instrument: access.SystemInstrument, ProjectID: projectIDs[2]}, projectIDs[2:])
		read(access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "member"}, nil)
		read(access.Principal{Kind: "user", Name: "reader", Via: "oauth", Scopes: []string{"apptown:change:check"}}, projectIDs[:1])
		read(access.Principal{Kind: "user", Name: "reader", Via: "oauth"}, nil)
	}
	// Enumeration writes nothing: an armed audit fault is still armed after it.
	clearAudit := dbtest.NextStatementFailure(db, "-- name: AppendReceipt")
	defer clearAudit()
	checkCases()
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return audit.At(time.Now()).RecordIn(context.Background(), tx, "boss", ids.Nil, "tripwire", "", nil)
	}); !errors.Is(err, dbtest.ErrInjectedFailure) {
		t.Fatalf("enumeration consumed audit tripwire: %v", err)
	}
	clearAudit()
	if _, err := s.Revoke(context.Background(), grant, "boss"); err != nil {
		t.Fatal(err)
	}
	read(admittedPrincipal(t, s, "reader"), nil)
	if _, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: fmt.Sprint(projectIDs[0])}, time.Now().Add(-time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	read(admittedPrincipal(t, s, "reader"), nil)
	disabled := true
	if err := s.SaveUser(context.Background(), AccountChange{Username: "reader", Disabled: &disabled}, admittedPrincipal(t, s, "boss")); err != nil {
		t.Fatal(err)
	}
	read(admittedPrincipal(t, s, "reader"), nil)
	read(access.Principal{Kind: "instrument", Name: "console/reader", Instrument: "console", Person: "reader", ProjectID: projectIDs[0]}, nil)
	for _, kind := range []string{"run", "console", "unknown"} {
		if _, err := s.VisibleProjects(context.Background(), access.Principal{Kind: "instrument", Name: "bad", Instrument: kind}, access.CapProjectRead); err == nil {
			t.Fatalf("unbound %s accepted", kind)
		}
	}
	for _, cap := range []access.Capability{access.CapChangeCreate, access.CapIdentityUsers, "unknown"} {
		if _, err := s.VisibleProjects(context.Background(), admittedPrincipal(t, s, "boss"), cap); err == nil {
			t.Fatalf("accepted %s", cap)
		}
	}
	for _, fragment := range []string{"-- name: ProjectAuthorityCatalogue", "-- name: ActiveGrants"} {
		clear := dbtest.NextStatementFailure(db, fragment)
		got, err := s.VisibleProjects(context.Background(), admittedPrincipal(t, s, "group-reader"), access.CapProjectRead)
		clear()
		if !errors.Is(err, dbtest.ErrInjectedFailure) || got != nil {
			t.Fatalf("lost %s failure: %v %v", fragment, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := s.VisibleProjects(ctx, admittedPrincipal(t, s, "boss"), access.CapProjectRead); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("cancelled: %v %v", got, err)
	}
}

// Benchmark checked visibility with operation-local facts. Setup is outside the timer;
// run with -run '^$' -bench BenchmarkVisibleProjects -benchtime=3x.
func BenchmarkVisibleProjects(b *testing.B) {
	for _, n := range []int{24, 100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			db := databaseSuite.New(b)
			s := New(db, Config{Projects: project.NewCatalogue(db)})
			if _, err := s.EnsureUser(context.Background(), "reader"); err != nil {
				b.Fatal(err)
			}
			if _, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeInstance}, ""); err != nil {
				b.Fatal(err)
			}
			if err := db.InTx(context.Background(), func(tx *database.Tx) error {
				for i := range n {
					if _, err := project.CreateAt(b.Context(), tx, time.Now(), project.Spec{Slug: fmt.Sprintf("project-%d", i)}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ids, err := s.VisibleProjects(context.Background(), testPrincipal("reader"), access.CapProjectRead)
				if err != nil || len(ids) != n {
					b.Fatalf("%d projects: %v %v", n, ids, err)
				}
			}
		})
	}
}

// Count the owner catalogue calls separately: its one-query projection belongs
// to Project, while the two authority reads below belong to Identity.
type countedCatalogue struct {
	projectCatalogue
	calls int
}

func (c *countedCatalogue) ProjectResources(ctx context.Context, id ids.ID) ([]access.Resource, error) {
	c.calls++
	return c.projectCatalogue.ProjectResources(ctx, id)
}

func TestVisibilityFactReadsDoNotGrowWithProjectCount(t *testing.T) {
	t.Parallel()
	// Three projects: a read per project already differs at three.
	for _, n := range []int{1, 3} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			db := testDB(t)
			catalogue := &countedCatalogue{projectCatalogue: project.NewCatalogue(db)}
			s := New(db, Config{Projects: catalogue})
			if _, err := s.EnsureUser(context.Background(), "reader"); err != nil {
				t.Fatal(err)
			}
			id, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeInstance}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := db.InTx(context.Background(), func(tx *database.Tx) error {
				for i := range n {
					if _, err := project.CreateAt(t.Context(), tx, time.Now(), project.Spec{Slug: fmt.Sprintf("count-%d", i)}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			q := &countedQueries{Handle: db.SQL()}
			got, err := s.visibleProjects(context.Background(), q, admittedPrincipal(t, s, "reader"), access.CapProjectRead)
			if err != nil || len(got) != n || q.reads != 2 || q.writes != 0 || catalogue.calls != 1 {
				t.Fatalf("visible=%d reads=%d writes=%d catalogue=%d: %v", len(got), q.reads, q.writes, catalogue.calls, err)
			}
			if _, err := s.Revoke(context.Background(), id, "reader"); err != nil {
				t.Fatal(err)
			}
			q.reads, catalogue.calls = 0, 0
			got, err = s.visibleProjects(context.Background(), q, admittedPrincipal(t, s, "reader"), access.CapProjectRead)
			if err != nil || len(got) != 0 || q.reads != 2 || catalogue.calls != 1 {
				t.Fatalf("next operation retained grants: %v reads=%d catalogue=%d: %v", got, q.reads, catalogue.calls, err)
			}
		})
	}
}

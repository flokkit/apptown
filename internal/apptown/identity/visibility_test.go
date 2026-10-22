package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

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
	if err := s.SaveGroup(context.Background(), GroupChange{Name: "family", Members: []string{"group-reader"}}, testPrincipal("boss")); err != nil {
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
	if _, err := s.Grant(context.Background(), Spec{SubjectType: "group", Subject: "family", Role: access.RoleAuditor, Scope: access.ScopeGroup, ScopeID: "family"}, ""); err != nil {
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
	// Leave a fault armed, then prove the first explicit receipt still hits it.
	clear := db.FailNextStatement("-- name: InsertDecision")
	defer clear()
	checkCases := func() {
		read(testPrincipal("boss"), projectIDs)
		read(testPrincipal("reader"), projectIDs[:1])
		read(testPrincipal("group-reader"), []ids.ID{projectIDs[0], projectIDs[2]})
		read(testPrincipal("outsider"), nil)
		read(access.Principal{Kind: "instrument", Name: "run/1", Instrument: "run", ProjectID: projectIDs[0]}, projectIDs[:1])
		read(access.Principal{Kind: "instrument", Name: "console/1", Instrument: "console", Person: "boss", ProjectID: projectIDs[1]}, projectIDs[1:2])
		read(access.Principal{Kind: "instrument", Name: "engine", Instrument: access.SystemInstrument}, projectIDs)
		read(access.Principal{Kind: "instrument", Name: "trigger", Instrument: access.SystemInstrument, ProjectID: projectIDs[2]}, projectIDs[2:])
		read(access.Principal{Kind: "user", Name: "boss", Via: "token", Role: "member"}, nil)
		read(access.Principal{Kind: "user", Name: "reader", Via: "oauth", Scopes: []string{"apptown:change:check"}}, projectIDs[:1])
		read(access.Principal{Kind: "user", Name: "reader", Via: "oauth"}, nil)
	}
	checkCases()
	if _, err := s.Decide(context.Background(), testPrincipal("outsider"), access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "instance"}}); err == nil || !strings.Contains(err.Error(), "forge_injected_failure") {
		t.Fatalf("enumeration consumed receipt tripwire: %v", err)
	}
	clear()
	clearAudit := db.FailNextStatement("-- name: AppendReceipt")
	defer clearAudit()
	checkCases()
	if err := db.InTx(context.Background(), func(tx *database.Tx) error {
		return audit.At(time.Now()).RecordIn(context.Background(), tx, "boss", ids.Nil, "tripwire", "", nil)
	}); !errors.Is(err, database.ErrInjectedFailure) {
		t.Fatalf("enumeration consumed audit tripwire: %v", err)
	}
	clearAudit()
	if _, err := s.Revoke(context.Background(), grant, "boss"); err != nil {
		t.Fatal(err)
	}
	read(testPrincipal("reader"), nil)
	if _, err := s.Grant(context.Background(), Spec{SubjectType: "user", Subject: "reader", Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: fmt.Sprint(projectIDs[0])}, time.Now().Add(-time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	read(testPrincipal("reader"), nil)
	projects := project.New(db, s)
	if err := projects.SetOwnerGroup(context.Background(), projectIDs[2], "elsewhere"); err != nil {
		t.Fatal(err)
	}
	read(testPrincipal("group-reader"), projectIDs[:1])
	disabled := true
	if err := s.SaveUser(context.Background(), AccountChange{Username: "reader", Disabled: &disabled}, testPrincipal("boss")); err != nil {
		t.Fatal(err)
	}
	read(testPrincipal("reader"), nil)
	read(access.Principal{Kind: "instrument", Name: "console/reader", Instrument: "console", Person: "reader", ProjectID: projectIDs[0]}, nil)
	for _, kind := range []string{"run", "console", "unknown"} {
		if _, err := s.VisibleProjects(context.Background(), access.Principal{Kind: "instrument", Name: "bad", Instrument: kind}, access.CapProjectRead); err == nil {
			t.Fatalf("unbound %s accepted", kind)
		}
	}
	for _, cap := range []access.Capability{access.CapChangeCreate, access.CapIdentityUsers, "unknown"} {
		if _, err := s.VisibleProjects(context.Background(), testPrincipal("boss"), cap); err == nil {
			t.Fatalf("accepted %s", cap)
		}
	}
	for _, fragment := range []string{"-- name: ProjectAuthorityCatalogue", "-- name: ActiveGrants"} {
		clear := db.FailNextStatement(fragment)
		got, err := s.VisibleProjects(context.Background(), testPrincipal("group-reader"), access.CapProjectRead)
		clear()
		if !errors.Is(err, database.ErrInjectedFailure) || got != nil {
			t.Fatalf("lost %s failure: %v %v", fragment, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := s.VisibleProjects(ctx, testPrincipal("boss"), access.CapProjectRead); !errors.Is(err, context.Canceled) || got != nil {
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
	ProjectCatalogue
	calls int
}

func (c *countedCatalogue) ProjectResources(ctx context.Context, id ids.ID) ([]access.Resource, error) {
	c.calls++
	return c.ProjectCatalogue.ProjectResources(ctx, id)
}

func TestVisibilityFactReadsDoNotGrowWithProjectCount(t *testing.T) {
	t.Parallel()
	// Three projects: a read per project already differs at three.
	for _, n := range []int{1, 3} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			db := testDB(t)
			catalogue := &countedCatalogue{ProjectCatalogue: project.NewCatalogue(db)}
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
			got, err := s.visibleProjects(context.Background(), q, testPrincipal("reader"), access.CapProjectRead)
			if err != nil || len(got) != n || q.reads != 2 || q.writes != 0 || catalogue.calls != 1 {
				t.Fatalf("visible=%d reads=%d writes=%d catalogue=%d: %v", len(got), q.reads, q.writes, catalogue.calls, err)
			}
			if _, err := s.Revoke(context.Background(), id, "reader"); err != nil {
				t.Fatal(err)
			}
			q.reads, catalogue.calls = 0, 0
			got, err = s.visibleProjects(context.Background(), q, testPrincipal("reader"), access.CapProjectRead)
			if err != nil || len(got) != 0 || q.reads != 2 || catalogue.calls != 1 {
				t.Fatalf("next operation retained grants: %v reads=%d catalogue=%d: %v", got, q.reads, catalogue.calls, err)
			}
		})
	}
}

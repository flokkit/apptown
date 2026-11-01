package project

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Personal order is computed before paging, uses stable principals across names,
// never expands project visibility.
func TestBrowsePersonalWorkBeforeHouseholdRecency(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	auth := identity.New(db, identity.Config{Projects: NewCatalogue(db)})
	people := map[string]context.Context{}
	principals := map[string]ids.ID{}
	for _, name := range []string{"alice", "bob", "newcomer"} {
		account, err := auth.EnsurePerson(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		principals[name] = account.PrincipalID
		people[name] = access.WithCaller(t.Context(), access.Caller{Principal: access.Principal{ID: account.PrincipalID, Kind: "user", Name: name}, Verified: true})
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	projects := map[string]ids.ID{}
	for _, name := range []string{"asked", "said", "uploaded", "household", "idle-a", "idle-b", "hidden"} {
		id, err := database.Transact(t.Context(), db, func(tx *database.Tx) (ids.ID, error) {
			return CreateAt(t.Context(), tx, at, Spec{Slug: name, Name: name})
		})
		if err != nil {
			t.Fatal(err)
		}
		projects[name] = id
		if name != "hidden" {
			for person := range people {
				if _, err := auth.Grant(t.Context(), identity.Spec{SubjectType: "user", Subject: person, Capability: string(access.CapProjectRead), Scope: access.ScopeProject, ScopeID: id.String()}, ""); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// sql-guarantee: preserve a historical opener name and time that current change operations cannot recreate.
	if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO changes(id,project_id,number,type,title,created_at,opened_by,opened_by_principal) VALUES($1,$2,1,'change','asked',$3,'old-alice-name',$4)`, ids.New(at), projects["asked"], at.Add(6*time.Hour), principals["alice"]); err != nil {
		t.Fatal(err)
	}
	// Two visible projects with runs at different times, so the fallback
	// order (everyone's newest work first) is exercised.
	for i, name := range []string{"idle-a", "household", "hidden"} {
		runID := ids.New(at)
		// sql-guarantee: current run operations cannot recreate fixed historical update times.
		if _, err := db.SQL().ExecContext(t.Context(), `INSERT INTO runs(id,project_id,number,workflow_name,workflow_version,kind,current_stage,definition_json,created_at,updated_at) VALUES($1,$2,1,'w',1,'pipeline','s','{}',$3,$4)`, runID, projects[name], at, at.Add(time.Duration(i+1)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []struct {
		person, project, kind string
		hour                  int
	}{
		{"alice", "said", "console.say", 5}, {"alice", "uploaded", "console.upload", 4},
		{"bob", "idle-b", "console.say", 10}, {"alice", "hidden", "console.say", 20},
		{"alice", "idle-a", "project.updated", 30}, // unrelated receipts are not work
	} {
		if err := audit.At(at.Add(time.Duration(event.hour)*time.Hour)).Record(people[event.person], db, event.person, projects[event.project], event.kind, "chat", nil); err != nil {
			t.Fatal(err)
		}
	}
	s := New(db, auth)
	// Exclusive history locks prove plain paths do not even read personal sources.
	// The old shared query waits on these locks and hits the deadline.
	func() {
		lock, err := db.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback()
		if _, err := lock.SQL().ExecContext(t.Context(), `LOCK TABLE audit_log, changes, runs, steps IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		plainContext, cancel := context.WithTimeout(people["alice"], 2*time.Second)
		defer cancel()
		visible, err := s.Visible(plainContext)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range visible {
			if p.LastWorkedAt != nil {
				t.Fatalf("Visible computed personal work for %s", p.Slug)
			}
		}
		for _, in := range []Browse{{Limit: 100}, {Limit: 100, PriorityIDs: []ids.ID{projects["said"]}}} {
			plain, err := s.Browse(plainContext, in)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range plain.Projects {
				if p.LastWorkedAt != nil {
					t.Fatalf("plain browse computed personal work for %s", p.Slug)
				}
			}
		}
	}()
	for _, tc := range []struct {
		person string
		want   []string
	}{
		{"alice", []string{"asked", "said", "uploaded", "household", "idle-a", "idle-b"}},
		{"bob", []string{"idle-b", "household", "idle-a", "asked", "said", "uploaded"}},
		{"newcomer", []string{"household", "idle-a", "asked", "idle-b", "said", "uploaded"}},
	} {
		page, err := s.Browse(people[tc.person], Browse{Limit: 100, Mine: true})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, p := range page.Projects {
			got = append(got, p.Slug)
		}
		if !reflect.DeepEqual(got, tc.want) || page.Total != 6 {
			t.Fatalf("%s: %v total %d, want %v", tc.person, got, page.Total, tc.want)
		}
		for i, p := range page.Projects {
			personal := tc.person == "alice" && i < 3 || tc.person == "bob" && i == 0
			if tc.person == "alice" && personal && p.LastWorkedAt != nil && !p.LastWorkedAt.Equal(at.Add(time.Duration(6-i)*time.Hour)) {
				t.Fatalf("%s latest time: %v", p.Slug, p.LastWorkedAt)
			}
			if (p.LastWorkedAt != nil) != personal {
				t.Fatalf("%s/%s personal time: %v", tc.person, p.Slug, p.LastWorkedAt)
			}
		}
	}
	page, err := s.Browse(people["alice"], Browse{Limit: 2, Offset: 3, Mine: true})
	if err != nil || len(page.Projects) != 2 || page.Projects[0].Slug != "household" || page.Projects[1].Slug != "idle-a" || page.Total != 6 {
		t.Fatalf("personal paging: %+v %v", page, err)
	}
	if _, err := s.Browse(context.Background(), Browse{Limit: 5, Mine: true}); err == nil {
		t.Fatal("unverified caller admitted")
	}
}

package project

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// BenchmarkProjectListsWithHistory includes authorization and row decoding on a
// private PostgreSQL database. Bulk fixtures bound setup cost; lifecycle
// operations cannot recreate historical change and run timestamps.
func BenchmarkProjectListsWithHistory(b *testing.B) {
	db := databaseSuite.New(b)
	auth := identity.New(db, identity.Config{Projects: NewCatalogue(db)})
	var people []ids.ID
	for i := range 20 {
		name := fmt.Sprintf("person-%d", i)
		if err := auth.SeedAdministrator(b.Context(), name); err != nil {
			b.Fatal(err)
		}
		person, err := auth.EnsurePerson(b.Context(), name)
		if err != nil {
			b.Fatal(err)
		}
		people = append(people, person.PrincipalID)
	}
	var projects []ids.ID
	if err := db.InTx(b.Context(), func(tx *database.Tx) error {
		for i := range 1000 {
			id, err := CreateAt(b.Context(), tx, time.Now(), Spec{Slug: fmt.Sprintf("project-%04d", i), Name: fmt.Sprintf("Project %04d", i)})
			if err != nil {
				return err
			}
			projects = append(projects, id)
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	payload := `{"message":"` + strings.Repeat("historical receipt ", 20) + `"}`
	for _, seed := range []struct {
		statement string
		count     int
		values    func(int) []any
	}{
		{
			// sql-guarantee: bulk query-scale fixtures avoid 200,000 individual audit owner calls before timing.
			statement: `INSERT INTO audit_log(id,at,actor,actor_principal,project_id,kind,payload_json) VALUES `,
			count:     200000,
			values: func(i int) []any {
				kind := "run.updated"
				if (i/20000)%5 == 0 {
					kind = "console.say"
				} else if (i/20000)%5 == 1 {
					kind = "console.upload"
				}
				return []any{ids.New(at), at.Add(time.Duration(i) * time.Minute), fmt.Sprintf("person-%d", (i/1000)%20), people[(i/1000)%20], projects[i%1000], kind, payload}
			},
		},
		{
			// sql-guarantee: current change operations cannot recreate these historical opening times.
			statement: `INSERT INTO changes(id,project_id,number,type,title,created_at,opened_by,opened_by_principal) VALUES `,
			count:     20000,
			values: func(i int) []any {
				return []any{ids.New(at), projects[i%1000], i/1000 + 1, "change", "Historical change", at.Add(time.Duration(i) * time.Hour), fmt.Sprintf("person-%d", (i/1000)%20), people[(i/1000)%20]}
			},
		},
		{
			// sql-guarantee: current run operations cannot recreate this bulk history with fixed timestamps.
			statement: `INSERT INTO runs(id,project_id,number,workflow_name,workflow_version,kind,current_stage,definition_json,created_at,updated_at) VALUES `,
			count:     20000,
			values: func(i int) []any {
				when := at.Add(time.Duration(i) * time.Hour)
				return []any{ids.New(at), projects[i%1000], i/1000 + 1, "w", 1, "pipeline", "s", "{}", when, when}
			},
		},
	} {
		if err := db.InTx(b.Context(), func(tx *database.Tx) error {
			for start := 0; start < seed.count; start += 1000 {
				var rows []string
				var args []any
				for i := start; i < min(start+1000, seed.count); i++ {
					values := seed.values(i)
					var placeholders []string
					for j := range values {
						placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)+j+1))
					}
					rows = append(rows, "("+strings.Join(placeholders, ",")+")")
					args = append(args, values...)
				}
				if _, err := tx.SQL().ExecContext(b.Context(), seed.statement+strings.Join(rows, ","), args...); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := db.SQL().ExecContext(b.Context(), `ANALYZE`); err != nil {
		b.Fatal(err)
	}
	ctx := access.WithCaller(b.Context(), access.Caller{Principal: access.Principal{ID: people[0], Kind: "user", Name: "person-0"}, Verified: true})
	s := New(db, auth)
	for _, name := range []string{"mine-limit-5", "plain-limit-5", "plain-visible-1000"} {
		b.Run(name, func(b *testing.B) {
			for range b.N {
				if name == "plain-visible-1000" {
					rows, err := s.Visible(ctx)
					if err != nil || len(rows) != 1000 {
						b.Fatalf("visible %d: %v", len(rows), err)
					}
				} else {
					page, err := s.Browse(ctx, Browse{Limit: 5, Mine: name == "mine-limit-5"})
					if err != nil || len(page.Projects) != 5 || page.Total != 1000 {
						b.Fatalf("browse: %+v %v", page, err)
					}
				}
			}
		})
	}
}

package apptown

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
)

// Real router and PostgreSQL; 1,000 idle projects, no provider I/O or work
// history. The separate populated SQL probe measures historical aggregation.
// Each refresh fetches the dashboard's three paths sequentially; concurrent
// clients share the application and connection pool, not authorization facts.
func BenchmarkDashboardRefresh(b *testing.B) {
	for _, clients := range []int{1, 8} {
		b.Run(fmt.Sprintf("1000-projects/%d-clients", clients), func(b *testing.B) {
			db := databaseSuite.New(b)
			a, err := New(context.Background(), db, Config{DataDir: b.TempDir(), Stock: testStock(b)})
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := a.ShutdownBackground(context.Background()); err != nil {
					b.Error(err)
				}
			})
			if err := a.Access.SeedAdministrator(context.Background(), "bench"); err != nil {
				b.Fatal(err)
			}
			if err := a.EnsureAuthorization(context.Background()); err != nil {
				b.Fatal(err)
			}
			if err := db.InTx(context.Background(), func(tx *database.Tx) error {
				for i := range 1000 {
					if _, err := project.CreateAt(b.Context(), tx, time.Now(), project.Spec{Slug: fmt.Sprintf("project-%d", i), Name: fmt.Sprintf("Project %d", i)}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			h := a.Handler()
			var next, latency, failures atomic.Int64
			var workers sync.WaitGroup
			b.ResetTimer()
			for range clients {
				workers.Add(1)
				go func() {
					defer workers.Done()
					for n := next.Add(1); n <= int64(b.N); n = next.Add(1) {
						start := time.Now()
						for _, path := range []string{"/api/v1/activity", "/api/v1/dashboard", "/api/v1/projects?limit=5&order=mine"} {
							if w := authzCall(h, "bench", "GET", path, ""); w.Code != 200 {
								failures.Add(1)
							}
						}
						latency.Add(time.Since(start).Nanoseconds())
					}
				}()
			}
			workers.Wait()
			b.StopTimer()
			b.ReportMetric(float64(latency.Load())/float64(b.N)/1e6, "ms/refresh")
			if failures.Load() != 0 {
				b.Fatalf("%d unsuccessful responses", failures.Load())
			}
		})
	}
}

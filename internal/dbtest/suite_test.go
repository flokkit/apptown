package dbtest_test

import (
	"context"
	"sync/atomic"
	"testing"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/dbtest"
)

func TestTemplateInitializesOnceAndClonesAreIndependent(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	suite := dbtest.NewSuite(func(_ context.Context, url string) (*database.DB, error) {
		calls.Add(1)
		db, err := database.Open(url)
		if err != nil {
			return nil, err
		}
		if _, err := db.Exec(`CREATE TABLE seed_probe(n int)`); err != nil {
			_ = db.Close()
			return nil, err
		}
		if _, err := db.Exec(`INSERT INTO seed_probe VALUES (42)`); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	})
	t.Cleanup(func() {
		if err := suite.Close(); err != nil {
			t.Fatal(err)
		}
	})
	for _, name := range []string{"one", "two", "three"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := suite.New(t)
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM seed_probe WHERE n=42`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("missing template seed: %d %v", count, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("initializer ran %d times", calls.Load())
			}
			if _, err := db.Exec(`DELETE FROM seed_probe`); err != nil {
				t.Fatal(err)
			}
		})
	}
}

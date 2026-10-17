package dependency

import (
	"context"
	"testing"

	"flokkit.io/forge/stock"
)

// Where a kind keeps its data is the catalogue's answer: a folder, "" for
// a kind it does not hold, and an error, never "", when it cannot be read.
func TestWhereANeedKeepsItsDataIsReadOrAnError(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	c := NewCatalog(db, nil)
	if _, _, err := c.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	rt := LocalRuntime{Catalog: c}
	if data, err := rt.Data(t.Context(), "postgres"); err != nil || data == "" {
		t.Fatalf("postgres: %q %v", data, err)
	}
	if data, err := rt.Data(t.Context(), "mystery"); err != nil || data != "" {
		t.Fatalf("a kind the catalogue does not hold: %q %v", data, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := rt.Data(t.Context(), "postgres"); err == nil {
		t.Fatalf("an unreadable catalogue answered %q", data)
	}
}

// A need's service spec carries where its emulator keeps its data, from the
// catalogue kind, so whatever starts it throwaway can give it that folder:
// S3's emulator exits without one. A kind with no local data carries none.
func TestAFulfilledNeedCarriesWhereItsEmulatorKeepsItsData(t *testing.T) {
	t.Parallel()
	c := NewCatalog(testDB(t), nil)
	if _, _, err := c.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	svcs, _, err := (LocalRuntime{Catalog: c}).Fulfil(t.Context(), []string{"s3", "redis"}, nil)
	if err != nil || len(svcs) != 2 {
		t.Fatalf("fulfilled %+v: %v", svcs, err)
	}
	for _, s := range svcs {
		want, err := (LocalRuntime{Catalog: c}).Data(t.Context(), s.Name)
		if err != nil {
			t.Fatal(err)
		}
		if s.Data != want {
			t.Errorf("%s carries data %q, its kind keeps it at %q", s.Name, s.Data, want)
		}
	}
	if svcs[0].Name != "s3" || svcs[0].Data != "/data" {
		t.Fatalf("s3's emulator keeps its data at /data: %+v", svcs[0])
	}
}

// A deployment's and a preview's Postgres make the app's user an ordinary
// owner; only a step's server (LocalRuntime.Steps), where the app's own
// tests run, makes it a superuser. The kind is remade on its volume when
// its start script or configuration moves, but not when its image does.
func TestOnlyAStepsPostgresMakesTheAppASuperuser(t *testing.T) {
	t.Parallel()
	c := NewCatalog(testDB(t), nil)
	if _, _, err := c.SyncStock(context.Background(), stock.FS, "dependencies"); err != nil {
		t.Fatal(err)
	}
	for _, steps := range []bool{false, true} {
		svcs, _, err := (LocalRuntime{Catalog: c, Steps: steps}).Fulfil(t.Context(), []string{"postgres"}, nil)
		if err != nil || len(svcs) != 1 {
			t.Fatalf("steps %v: %+v %v", steps, svcs, err)
		}
		want := "false"
		if steps {
			want = "true"
		}
		if got := svcs[0].Env["APP_SUPERUSER"]; got != want {
			t.Errorf("steps %v: APP_SUPERUSER %q, want %q", steps, got, want)
		}
		if !svcs[0].ConfigAtStart || !svcs[0].KeepsImage || len(svcs[0].Entrypoint) == 0 {
			t.Errorf("steps %v: the start script is set at every start and the image is kept: %+v", steps, svcs[0])
		}
	}
}

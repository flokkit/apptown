package adoption

import (
	"errors"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/dbtest"
)

func TestLibraryManifestRulesAndAtomicPut(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	lib := wow.NewLibrary(db)
	ads := newLibrary(db, wow.New(db, wow.Dependencies{}))
	pid := adoptionTestProject(t, db, "m", "M")
	a, _ := lib.Create(t.Context(), "lang/go", "go", "", "alice")
	_ = lib.Bump(t.Context(), a.ID, "go v2", "", "alice", a.Name)
	b, _ := lib.Create(t.Context(), "prose/plain", "plain", "", "alice")

	if _, err := lib.Resolve(t.Context(), []wow.Pin{{Name: "lang/go"}, {Name: "lang/go"}}); !errors.Is(err, wow.ErrImportedTwice) {
		t.Fatalf("twice: %v", err)
	}
	if _, err := lib.Resolve(t.Context(), []wow.Pin{{Name: "lang/rust"}}); !errors.Is(err, wow.ErrUnknownBlock) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := lib.Resolve(t.Context(), []wow.Pin{{Name: "lang/go", Version: 3}}); !errors.Is(err, wow.ErrNoSuchVersion) {
		t.Fatalf("version: %v", err)
	}
	pins, err := lib.Resolve(t.Context(), []wow.Pin{{Name: "lang/go"}, {Name: "prose/plain", Version: 1, Disabled: true}})
	if err != nil || pins[0].Version != 2 || pins[0].BlockID != a.ID || !pins[1].Disabled || pins[1].BlockID != b.ID {
		t.Fatalf("resolved: %+v %v", pins, err)
	}
	if err := ads.PutImports(t.Context(), pid, pins); err != nil {
		t.Fatal(err)
	}
	text, hash, imports, err := ads.Compile(t.Context(), pid)
	if err != nil || len(imports) != 2 || text != "# lang/go v2\n\ngo v2" || hash == "" {
		t.Fatalf("compile skips disabled imports: %q %v %v", text, imports, err)
	}
	// A replacement that fails half-way leaves the old manifest standing.
	dbtest.FailNextStatement(t, db, "INSERT INTO guidance_imports")
	if err := ads.PutImports(t.Context(), pid, pins[:1]); err == nil {
		t.Fatal("a half-written manifest was reported as saved")
	}
	if im := importsOf(t, db, pid); len(im) != 2 {
		t.Fatalf("half-written manifest replaced the old one: %+v", im)
	}
}

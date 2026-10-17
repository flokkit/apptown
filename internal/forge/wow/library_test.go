package wow

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"flokkit.io/forge/internal/dbtest"
)

func TestLibraryCreateAndBump(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	lib := NewLibrary(db)
	if _, err := lib.Create(t.Context(), "Bad Name", "body", "", "alice"); !errors.Is(err, ErrBlockName) {
		t.Fatalf("a name outside the shape is refused: %v", err)
	}
	b, err := lib.Create(t.Context(), "prose/plain-words", "Say it plainly.", " tone , prose,", "alice")
	if err != nil || b.LatestVersion != 1 || b.Tags != "tone,prose" {
		t.Fatalf("create: %+v %v", b, err)
	}
	if created := wowReceipts(t, db, "guidance.block.created", "prose/plain-words"); len(created) != 1 || created[0].Kind != "guidance.block.created" || created[0].Actor != "alice" {
		t.Fatalf("create receipt: %+v", created)
	}
	if err := lib.Bump(t.Context(), b.ID, "Say it plainly, always.", "", "bob", b.Name); err != nil {
		t.Fatal(err)
	}
	again, _ := lib.ByID(t.Context(), b.ID)
	body, _ := lib.VersionBody(t.Context(), b.ID, 2)
	if again.LatestVersion != 2 || body != "Say it plainly, always." {
		t.Fatalf("bump: %+v %q", again, body)
	}
	versions, _ := lib.Versions(t.Context(), b.ID)
	if len(versions) != 2 || versions[0].Version != 2 {
		t.Fatalf("history newest first: %+v", versions)
	}
	// A failed receipt rolls the version back with it.
	dbtest.FailNextStatement(t, db, "INSERT INTO audit_log")
	if err := lib.Bump(t.Context(), b.ID, "third", "", "bob", b.Name); err == nil {
		t.Fatal("a bump without its receipt was reported as done")
	}
	if versions, err := lib.Versions(t.Context(), b.ID); err != nil || len(versions) != 2 {
		t.Fatalf("a failed bump kept its version: %+v %v", versions, err)
	}
}

// Concurrent edits must allocate distinct durable versions on the same block.
func TestGuidanceVersionsDistinctUnderRace(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	lib := NewLibrary(db)
	b, err := lib.Create(t.Context(), "house/rule", "v1", "", "bls")
	if err != nil {
		t.Fatal(err)
	}
	const edits = 6
	start := make(chan struct{})
	errors := make([]error, edits)
	var wg sync.WaitGroup
	for i := range edits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errors[i] = lib.Bump(t.Context(), b.ID, fmt.Sprintf("body %d", i), "", "bls", b.Name)
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	versions, err := lib.Versions(t.Context(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	distinct := map[int]bool{}
	for _, v := range versions {
		distinct[v.Version] = true
	}
	if len(distinct) != edits+1 || len(versions) != edits+1 {
		t.Fatalf("versions under race: %+v", versions)
	}
	if latest, err := lib.ByID(t.Context(), b.ID); err != nil || latest.LatestVersion != edits+1 {
		t.Fatalf("latest version: %+v %v", latest, err)
	}
}

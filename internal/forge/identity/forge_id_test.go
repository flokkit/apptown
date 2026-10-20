package identity

import (
	"context"
	"sync"
	"testing"
	"time"

	"flokkit.io/forge/internal/ids"
)

// AppTown's id is minted once: every later ask, and every ask racing the
// first, reads the same UUID.
func TestTheForgesIDIsMintedOnce(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	var wg sync.WaitGroup
	got := make([]ids.ID, 4)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := ForgeIDIn(context.Background(), db, time.Now())
			if err != nil {
				t.Error(err)
			}
			got[i] = id
		}()
	}
	wg.Wait()
	later, err := ForgeIDIn(context.Background(), db, time.Now().Add(time.Hour))
	if err != nil || later.IsZero() {
		t.Fatalf("AppTown's id: %s %v", later, err)
	}
	for _, id := range got {
		if id != later {
			t.Fatalf("AppTown's id changed: %s then %s", id, later)
		}
	}
}

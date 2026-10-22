package identity

import (
	"context"
	"testing"

	"github.com/flokkit/apptown/internal/access"
)

// AppTown's own acts read as "AppTown" in history, whether they were recorded
// under its names from before the AppTown rename or after; the recorded name
// is not rewritten. A run's identity and a person read as before.
//
// Kills: ShowActorsIn returning a system actor's recorded name.
func TestAppTownsOwnActsReadAsAppTown(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	actors := []access.Actor{{Name: "forge"}, {Name: "forge-system"}, {Name: "system"}, {Name: "apptown-system"}, {Name: "apptown"}, {Name: "run/7"}}
	shown, err := ShowActorsIn(context.Background(), db, actors)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"AppTown", "AppTown", "AppTown", "AppTown", "AppTown", "run/7"} {
		if shown[i] != want {
			t.Errorf("%q reads %q, want %q", actors[i].Name, shown[i], want)
		}
	}
	if actors[0].Name != "forge" {
		t.Fatal("the recorded actor was rewritten")
	}
}

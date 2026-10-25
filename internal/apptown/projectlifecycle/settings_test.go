package projectlifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flokkit/apptown/internal/apptown/project"
)

func TestSettingsSharesNameReservationsAndKeepsBestEffortAudit(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, false)
	p, err := f.lifecycle.Create(context.Background(), Draft{Slug: "settings", Name: "Settings", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	name, icon := "Renamed", "new"
	rev := p.SettingsRevision
	release, err := f.lifecycle.ReserveNames(p.Slug)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.lifecycle.UpdateSettings(context.Background(), p.ID, project.SettingsPatch{ExpectedRevision: &rev, Name: &name, Icon: &icon}, "alice")
	release()
	if !errors.Is(err, ErrNameBusy) {
		t.Fatalf("an edit while the project's name is held elsewhere: %v", err)
	}
	before, err := f.lifecycle.projects.ByID(context.Background(), p.ID)
	if err != nil || before.Name != p.Name || before.Icon != "" {
		t.Fatalf("busy edit wrote: %+v %v", before, err)
	}
	cleanup := f.db.FailNextStatement("INSERT INTO audit_log")
	defer cleanup()
	after, err := f.lifecycle.UpdateSettings(context.Background(), p.ID, project.SettingsPatch{ExpectedRevision: &rev, Name: &name, Icon: &icon}, "alice")
	if err != nil || after.Name != name || after.Icon != icon {
		t.Fatalf("best effort audit changed command success: %+v %v", after, err)
	}
}

// A project's slug names its repository's folder and its outside resources,
// none of which a rename moves. Renaming was allowed, and left the folder
// behind for the next project given the old slug to adopt (source and
// history). The slug is now fixed; the display name still changes, and a
// patch may repeat the slug it already has.
func TestAProjectsSlugCannotChange(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, false)
	ctx := t.Context()
	a, err := f.lifecycle.Create(ctx, Draft{Slug: "shop", Name: "Shop", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	dir := hubDir(f.lifecycle.dataDir, a.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private-to-first-project.txt"), []byte("first project's source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.lifecycle.InitializeRepository(ctx, a, "main", ""); err != nil {
		t.Fatal(err)
	}
	next, rev := "renamed-shop", a.SettingsRevision
	if _, err := f.lifecycle.UpdateSettings(ctx, a.ID, project.SettingsPatch{ExpectedRevision: &rev, Slug: &next}, "alice"); !errors.Is(err, project.ErrSlugFixed) {
		t.Fatalf("a slug change: %v", err)
	}
	if _, err := f.lifecycle.Create(ctx, Draft{Slug: "shop", Name: "Different shop", Actor: "alice"}); !errors.Is(err, project.ErrSlugTaken) {
		t.Fatalf("a second project took the first one's slug: %v", err)
	}
	name, same := "Shop and more", a.Slug
	after, err := f.lifecycle.UpdateSettings(ctx, a.ID, project.SettingsPatch{ExpectedRevision: &rev, Slug: &same, Name: &name}, "alice")
	if err != nil || after.Slug != "shop" || after.Name != name {
		t.Fatalf("a name edit that repeats the slug: %+v %v", after, err)
	}
}

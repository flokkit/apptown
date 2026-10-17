package projectlifecycle

import (
	"context"
	"errors"
	"testing"

	"flokkit.io/forge/internal/forge/project"
)

func TestSettingsSharesNameReservationsAndKeepsBestEffortAudit(t *testing.T) {
	t.Parallel()
	f := newOwnerFixture(t, false)
	p, err := f.lifecycle.Create(context.Background(), Draft{Slug: "settings", Name: "Settings", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	slug, icon := "renamed", "new"
	rev := p.SettingsRevision
	release, err := f.lifecycle.ReserveNames(slug)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.lifecycle.UpdateSettings(context.Background(), p.ID, project.SettingsPatch{ExpectedRevision: &rev, Slug: &slug, Icon: &icon}, "alice")
	release()
	if !errors.Is(err, ErrNameBusy) {
		t.Fatalf("reserved name admitted: %v", err)
	}
	before, err := f.lifecycle.projects.ByID(context.Background(), p.ID)
	if err != nil || before.Slug != p.Slug || before.Icon != "" {
		t.Fatalf("busy edit wrote: %+v %v", before, err)
	}
	cleanup := f.db.FailNextStatement("INSERT INTO audit_log")
	defer cleanup()
	after, err := f.lifecycle.UpdateSettings(context.Background(), p.ID, project.SettingsPatch{ExpectedRevision: &rev, Slug: &slug, Icon: &icon}, "alice")
	if err != nil || after.Slug != slug || after.Icon != icon {
		t.Fatalf("best effort audit changed command success: %+v %v", after, err)
	}
}

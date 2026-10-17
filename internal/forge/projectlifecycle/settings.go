package projectlifecycle

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/project"
)

// UpdateSettings is the complete settings edit. Its caller authorizes the edit;
// lifecycle coordinates names with creation and purge, project owns persistence.
func (s *Service) UpdateSettings(ctx context.Context, id ids.ID, in project.SettingsPatch, actor string) (*project.Project, error) {
	if in.Slug != nil && !project.ValidSlug(*in.Slug) {
		return nil, project.ErrSlug
	}
	observed, err := s.projects.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	names := []string{observed.Slug}
	if in.Slug != nil {
		names = append(names, *in.Slug)
	}
	release, err := s.ReserveNames(names...)
	if err != nil {
		return nil, err
	}
	defer release()
	var changed project.SettingsChange
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		var err error
		changed, err = project.UpdateSettingsIn(ctx, tx, id, observed.Slug, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	before, after := changed.Before, changed.After
	details := map[string]any{}
	if before.Name != after.Name {
		details["name"] = map[string]string{"from": before.Name, "to": after.Name}
	}
	if before.Slug != after.Slug {
		details["slug"] = map[string]string{"from": before.Slug, "to": after.Slug}
	}
	if in.Icon != nil {
		details["icon"] = after.Icon
	}
	if in.Blurb != nil {
		details["blurb"] = after.Blurb
	}
	if in.Namespace != nil {
		details["namespace"] = after.Namespace
	}
	if len(details) > 0 {
		audit.At(s.now()).BestEffort(ctx, s.db, actor, id, "project.renamed", after.Slug, details)
	}
	return &after, nil
}

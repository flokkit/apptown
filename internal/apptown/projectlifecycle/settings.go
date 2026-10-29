package projectlifecycle

import (
	"context"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
)

// UpdateSettings is the complete settings edit. Its caller authorizes the edit;
// lifecycle coordinates names with creation and purge, project owns persistence.
// A new address moves the project there; the old one names nothing afterwards.
// The edit and its receipt commit together.
func (s *Service) UpdateSettings(ctx context.Context, id ids.ID, in project.SettingsPatch, actor string) (*project.Project, error) {
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
	var after project.Project
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		changed, err := project.UpdateSettingsIn(ctx, tx, id, observed.Slug, in)
		if err != nil {
			return err
		}
		before := changed.Before
		after = changed.After
		details := map[string]any{}
		if before.Slug != after.Slug {
			details["address"] = map[string]string{"from": before.Slug, "to": after.Slug}
		}
		if before.Name != after.Name {
			details["name"] = map[string]string{"from": before.Name, "to": after.Name}
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
		if len(details) == 0 {
			return nil
		}
		return audit.At(s.now()).RecordIn(ctx, tx, actor, id, "project.renamed", after.Slug, details)
	})
	if err != nil {
		return nil, err
	}
	return &after, nil
}

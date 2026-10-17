package project

import (
	"context"
	"errors"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/project/internal/projectsql"
	"flokkit.io/forge/internal/strs"
)

type SettingsPatch struct {
	// ExpectedRevision is the settings revision the edit started from. It is
	// required: without it an edit could overwrite changes it never saw.
	ExpectedRevision *int64  `json:"expectedRevision"`
	Name             *string `json:"name"`
	Slug             *string `json:"slug"`
	Namespace        *string `json:"namespace"`
	Icon             *string `json:"icon"`
	Blurb            *string `json:"blurb"`
}
type SettingsChange struct{ Before, After Project }

var (
	ErrSettingsChanged          = fault.New(fault.Conflict, "project name changed; retry the edit")
	ErrSettingsRevisionRequired = fault.New(fault.Invalid, "send the settings revision the edit started from (expectedRevision)")
	// ErrSettingsMoved carries a code so a client can tell it from other
	// conflicts, such as a taken slug, and offer the person the new values.
	ErrSettingsMoved error = settingsMoved{}
)

type settingsMoved struct{}

func (settingsMoved) Error() string {
	return "the project's settings changed since this edit started; review the current values before saving again"
}
func (settingsMoved) FaultKind() fault.Kind { return fault.Conflict }
func (settingsMoved) FaultCode() string     { return "settings-moved" }

// UpdateSettingsIn joins lifecycle's transaction after it reserves both names.
// The row lock makes the patch apply to the current record; fields omitted by
// the caller are not overwritten with an earlier snapshot. The patch must name
// the settings revision it started from; a record that has moved on since is
// refused with ErrSettingsMoved before anything is written, and a write
// advances the revision.
func UpdateSettingsIn(ctx context.Context, tx *database.Tx, id ids.ID, reservedSlug string, in SettingsPatch) (SettingsChange, error) {
	var result SettingsChange
	q := queries(tx)
	held, err := q.ProjectByIDForUpdate(ctx, id)
	if err != nil {
		return result, err
	}
	// A project being imported or deleted, or deleted, keeps its settings
	// (and its address) until it is active again or purged.
	if err := RefuseUnlessActiveIn(ctx, tx, id); err != nil {
		return result, err
	}
	result.Before = record(held)
	if held.Slug != reservedSlug {
		return result, ErrSettingsChanged
	}
	if in.ExpectedRevision == nil {
		return result, ErrSettingsRevisionRequired
	}
	if *in.ExpectedRevision != held.SettingsRevision {
		return result, ErrSettingsMoved
	}
	if in.Slug != nil {
		if !ValidSlug(*in.Slug) {
			return result, ErrSlug
		}
		held.Slug = *in.Slug
	}
	if in.Name != nil && *in.Name != "" {
		held.Name = *in.Name
	}
	if in.Icon != nil {
		held.Icon = *in.Icon
	}
	if in.Namespace != nil {
		held.Namespace = *in.Namespace
	}
	if in.Blurb != nil {
		held.Blurb = strs.Truncate(*in.Blurb, 140)
	}
	if record(held) == result.Before {
		result.After = result.Before
		return result, nil
	}
	updated, err := q.UpdateProjectSettings(ctx, projectsql.UpdateProjectSettingsParams{ID: id, Slug: held.Slug, Name: held.Name, Icon: held.Icon, Blurb: held.Blurb, Namespace: held.Namespace})
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "23505" {
		return result, ErrSlugTaken
	}
	if err != nil {
		return result, err
	}
	if updated.Slug != result.Before.Slug {
		if err := requireSlugFreeIn(ctx, tx, updated.Slug); err != nil {
			return result, err
		}
	}
	result.After = record(updated)
	return result, nil
}

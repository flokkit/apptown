package datalayout

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/project/repositorylayout"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// Journal paths are checked again before resuming effects. A partial or edited
// journal cannot turn owned migration operations into arbitrary file movement.
func (j journal) validatePlan() error {
	owners := map[ids.ID]repositorylayout.Mapping{}
	for _, m := range j.Inventory.Mappings {
		if m.ID.IsZero() {
			return errors.New("invalid journal repository owner")
		}
		if _, found := owners[m.ID]; found {
			return errors.New("duplicate journal repository owner")
		}
		owners[m.ID] = m
	}
	for _, r := range j.Inventory.References {
		if _, found := owners[r.Owner]; !found {
			return errors.New("journal reference has unknown project owner")
		}
		if !below(j.OriginalRoot, r.Before) || !below(j.Root, r.After) {
			return errors.New("journal reference escapes installation roots")
		}
	}
	for _, edit := range j.Inventory.Publications {
		m, found := owners[edit.Before.ProjectID]
		ref := m.Reference(j.OriginalRoot, j.Root)
		if !found || edit.Before.ID.IsZero() || edit.Before.Repository != ref.Before || edit.After != ref.After {
			return errors.New("journal publication location differs from project mapping")
		}
	}
	for _, edit := range j.Inventory.Consoles {
		relative, err := filepath.Rel(filepath.Join(j.Root, "console"), edit.File)
		if err != nil || strings.Count(relative, string(filepath.Separator)) != 1 || filepath.Base(relative) != "session.json" || !below(filepath.Join(j.Root, "console"), edit.File) || !below(j.OriginalRoot, edit.Before) || !below(j.Root, edit.After) || !digest(edit.BeforeDigest) || !digest(edit.AfterDigest) {
			return errors.New("invalid journal console edit")
		}
	}
	for _, edit := range j.Inventory.Git {
		if !below(j.Root, edit.Repository) || !digest(edit.BeforeDigest) || !digest(edit.AfterDigest) {
			return errors.New("invalid journal Git edit")
		}
		for _, change := range edit.Changes {
			if !below(j.Root, change.After) {
				return errors.New("journal Git target escapes installation")
			}
		}
	}
	return nil
}
func below(root, path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && strings.HasPrefix(path, root+string(filepath.Separator))
}
func digest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32
}

func (j journal) verifyOwners(ctx context.Context, db *database.DB) error {
	owners, err := project.RepositoryOwnersIn(ctx, db)
	if err != nil {
		return err
	}
	if len(owners) != len(j.Inventory.Mappings) {
		return errors.New("project ownership changed while relocation was pending")
	}
	expected := map[ids.ID]repositorylayout.Mapping{}
	for _, mapping := range j.Inventory.Mappings {
		expected[mapping.ID] = mapping
	}
	for _, owner := range owners {
		_, recorded, err := scm.ReadMain(ctx, db, owner.Ref.ID)
		if err != nil {
			return err
		}
		observed := repositorylayout.Mapping{ID: owner.Ref.ID, Missing: missingRepositoryAllowed(owner, recorded)}
		if mapping, found := expected[owner.Ref.ID]; !found || mapping != observed {
			return fmt.Errorf("project %s ownership changed while relocation was pending", owner.Ref.ID)
		}
	}
	return nil
}

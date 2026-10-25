package datalayout

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
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
		if m.ID.IsZero() || !project.ValidSlug(m.Slug) || m.SourceUUID != (j.FromVersion == Version) {
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
	for _, main := range j.Inventory.Mains {
		if _, found := owners[main.ProjectID]; !found {
			return errors.New("journal main evidence has unknown owner")
		}
	}
	count := len(j.Inventory.Linked) + len(j.Inventory.Borrowed)
	if len(j.Exchanges) > count {
		return errors.New("journal has unowned metadata exchanges")
	}
	for index, linked := range j.Inventory.Linked {
		finalHub := ""
		for _, m := range owners {
			if linked.Hub == m.Source(j.Root) {
				finalHub = m.Reference(j.Root, j.Root).After
				break
			}
		}
		if finalHub == "" || !below(j.Root, linked.Folder) || linked.OriginalRoot != j.OriginalRoot || linked.CurrentRoot != j.Root || filepath.Clean(linked.Admin) != linked.Admin || !strings.HasPrefix(linked.Admin, "worktrees"+string(filepath.Separator)) || strings.Count(linked.Admin, string(filepath.Separator)) != 1 {
			return errors.New("invalid journal linked workspace owner")
		}
		if index >= len(j.Exchanges) {
			continue
		}
		exchange := j.Exchanges[index]
		work := filepath.Join(j.Root, metadataName, j.Nonce, strconv.Itoa(index))
		if exchange.Kind != "linked" || exchange.OriginalDirectoryDigest != "" || exchange.Folder != linked.Folder || exchange.Staged != filepath.Join(work, "stage", ".git") || exchange.Backup != filepath.Join(work, "backup") || exchange.Admin != filepath.Join(finalHub, ".git", linked.Admin) || !digest(exchange.PointerDigest) || !digest(exchange.MetadataDigest) || !digest(exchange.AdminDigest) {
			return fmt.Errorf("metadata exchange %d differs from owned staging plan", index)
		}
	}

	for offset, borrowed := range j.Inventory.Borrowed {
		found := false
		for _, m := range owners {
			if borrowed.Hub == m.Source(j.Root) {
				found = true
				break
			}
		}
		if !found || !below(j.Root, borrowed.Folder) || borrowed.OriginalRoot != j.OriginalRoot || borrowed.CurrentRoot != j.Root || !digest(borrowed.MetadataDigest) || !digest(borrowed.ObjectDigest) {
			return errors.New("invalid journal borrowed workspace owner")
		}
		index := len(j.Inventory.Linked) + offset
		if index >= len(j.Exchanges) {
			continue
		}
		exchange := j.Exchanges[index]
		work := filepath.Join(j.Root, metadataName, j.Nonce, strconv.Itoa(index))
		if exchange.Kind != "borrowed" || exchange.Folder != borrowed.Folder || exchange.Staged != filepath.Join(work, "stage", ".git") || exchange.Backup != filepath.Join(work, "backup") || exchange.OriginalDirectoryDigest != borrowed.MetadataDigest || exchange.Admin != "" || exchange.AdminDigest != "" || exchange.PointerDigest != "" || !digest(exchange.MetadataDigest) {
			return errors.New("borrowed metadata exchange differs from owned staging plan")
		}
	}
	if j.Phase != "planned" && j.Phase != "staging" && len(j.Exchanges) != count {
		return errors.New("journal advanced with incomplete metadata staging")
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
		return errors.New("project ownership changed while layout migration was pending")
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
		observed := repositorylayout.Mapping{ID: owner.Ref.ID, Slug: owner.Ref.Slug, SourceUUID: j.FromVersion == Version, Missing: missingRepositoryAllowed(owner, recorded)}
		if mapping, found := expected[owner.Ref.ID]; !found || mapping != observed {
			return fmt.Errorf("project %s ownership changed while layout migration was pending", owner.Ref.ID)
		}
	}
	return nil
}

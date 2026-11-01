package projectlifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/gitexec"
)

// CopyDraft is a throwaway copy of a project at one commit: an eval's arm
// (design/subsystems/conformance.md).
type CopyDraft struct {
	Slug, Name string
	SourceID   ids.ID
	// Commit is where the copy starts: the source's history up to it and
	// nothing after, so work done since is not present.
	Commit string
	// Record writes the copy record in the creation's own transaction, so
	// no copy ever exists unmarked.
	Record func(tx *database.Tx, projectID ids.ID) error
	Who    string
	// Packs is the pack picks the copy adopts, exactly as the source would
	// with them (adoption.Service.PrepareCopyWith); nil is the source's
	// adoption as it is now.
	Packs []adoption.PackPick
	// Revision, with no Packs, is the source's recorded adoption revision
	// the copy reproduces instead of its current one
	// (adoption.Service.PrepareCopyAt): where an adoption Change it
	// re-runs started.
	Revision int64
}

// CreateCopy makes a throwaway copy of a project: a new project with no
// members, its repository started from a bundle of the source's history
// cut at Commit, and exactly the adoption the source is on now, or would
// have with in.Packs (adoption.Service.PrepareCopyWith). It is given nothing else: no secrets, no
// integrations, no deployments. Its purpose (project.EvalCopy) is in the
// project's own INSERT, and the eval's copy record is written in the same
// transaction. A failure after the project exists returns the project with the
// error, so the caller can account for it and delete it.
func (s *Service) CreateCopy(ctx context.Context, in CopyDraft) (*project.Project, error) {
	if in.Record == nil || in.Commit == "" || strings.TrimSpace(in.Who) == "" {
		return nil, errors.New("a copy names its source commit, its record and who made it")
	}
	source, err := project.ByIDIn(ctx, s.db, in.SourceID)
	if err != nil {
		return nil, fmt.Errorf("reading the source project: %w", err)
	}
	if source.DefaultBranch == "" {
		return nil, fmt.Errorf("project %s is not repo-backed: there is nothing to copy", source.Slug)
	}
	if !project.ValidSlug(in.Slug) {
		return nil, errInvalidSlug
	}
	release, err := s.reserveNames(in.Slug)
	if err != nil {
		return nil, err
	}
	defer release()
	receipt, err := s.adoption.Union(ctx, ids.Nil, nil)
	if err != nil {
		return nil, err
	}
	var p project.Project
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		at := s.now()
		group, err := identity.SovereignGroupIn(ctx, tx, at)
		if err != nil {
			return err
		}
		id, err := project.CreateAt(ctx, tx, at, project.Spec{Slug: in.Slug, Name: in.Name, Namespace: "evals", Purpose: project.EvalCopy, Blurb: "A throwaway copy of " + source.Name + " made for an eval",
			OwnerGroup: group, DefaultApprovers: "role:" + access.RoleProjectApprover + ",role:" + access.RoleProjectOwner})
		if err != nil {
			return err
		}
		if err := in.Record(tx, id); err != nil {
			return err
		}
		if err := adoption.InitializeIn(ctx, tx, id, 0, ""); err != nil {
			return err
		}
		if err := adoption.RecordIn(ctx, tx, id, 1, adoption.AdoptionCreate, "eval copy created", receipt, in.Who, at); err != nil {
			return err
		}
		if err := audit.At(at).RecordIn(ctx, tx, in.Who, id, "project.copied", in.Slug, map[string]any{"from": source.Slug, "commit": in.Commit}); err != nil {
			return err
		}
		p, err = project.ByIDIn(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	spool := filepath.Join(s.dataDir, "spool")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		return &p, err
	}
	bundle := filepath.Join(spool, fmt.Sprintf("copy-%d.bundle", p.ID))
	defer os.Remove(bundle)
	hub, err := source.Repository(s.dataDir)
	if err != nil {
		return &p, err
	}
	if err := gitexec.BundleCommit(ctx, hub, in.Commit, source.DefaultBranch, bundle); err != nil {
		return &p, fmt.Errorf("cutting %s's history at %s: %w", source.Slug, in.Commit, err)
	}
	if err := s.seed(ctx, &p, in.Who, scm.ViaCopy, map[string]any{"source": "copy", "from": source.Slug, "commit": in.Commit}, func(dir string) (string, error) {
		return scm.SeedFromBundle(ctx, dir, bundle, source.DefaultBranch)
	}); err != nil {
		return &p, err
	}
	seeded, err := project.ByIDIn(ctx, s.db, p.ID)
	if err != nil {
		return &p, err
	}
	var c *adoption.Candidate
	if in.Revision != 0 && in.Packs == nil {
		c, err = s.adoption.PrepareCopyAt(ctx, p.ID, source.ID, in.Revision)
	} else {
		c, err = s.adoption.PrepareCopyWith(ctx, p.ID, source.ID, in.Packs)
	}
	if err != nil {
		return &seeded, err
	}
	if _, err := s.applyCandidateForChange(adoption.WithSystemAuthority(ctx, p.ID), &seeded, c, in.Who, "copied from "+source.Slug, ids.Nil, adoption.OperationApplyPack); err != nil {
		return &seeded, fmt.Errorf("giving the copy %s's way of working: %w", source.Slug, err)
	}
	return &seeded, nil
}

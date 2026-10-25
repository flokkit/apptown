package projectlifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

func (s *Service) renameRequested(ctx context.Context, id ids.ID, in project.SettingsPatch) (*project.Project, error) {
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified || caller.Principal.ID.IsZero() {
		return nil, access.ErrPrincipalRequired
	}
	if s.identity == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	// Preserve the settings door's existing administrator admission.
	if _, err := s.identity.Require(ctx, caller.Principal, access.Permission{Capability: access.CapIdentityAuth, Resource: access.Resource{Type: "instance"}}); err != nil {
		return nil, err
	}
	if in.Slug == nil {
		return nil, fault.New(fault.Invalid, "an address request needs its requested slug")
	}
	input := in
	input.RequestID = ids.Nil
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var out project.Project
	var releaseNames func()
	defer func() {
		if releaseNames != nil {
			releaseNames()
		}
	}()
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		claim, err := project.ClaimRenameIn(ctx, tx, s.now(), caller.Principal.ID, in.RequestID, id, hash)
		if err != nil {
			return err
		}
		if err := project.RefuseUnlessActiveIn(ctx, tx, id); err != nil {
			return err
		}
		observed, err := project.ByIDIn(ctx, tx, id)
		if err != nil {
			return err
		}
		if !claim.Fresh {
			out = claim.Result
			return nil
		}
		releaseNames, err = s.ReserveNames(observed.Slug, *in.Slug)
		if err != nil {
			return err
		}
		change, err := project.UpdateSettingsIn(ctx, tx, id, observed.Slug, in)
		if err != nil {
			return err
		}
		actor := caller.Receipt
		if actor == "" {
			actor = caller.Principal.Name
		}
		details := map[string]any{"address": map[string]string{"from": change.Before.Slug, "to": change.After.Slug}, "name": map[string]string{"from": change.Before.Name, "to": change.After.Name}}
		if in.Icon != nil {
			details["icon"] = change.After.Icon
		}
		if in.Namespace != nil {
			details["namespace"] = change.After.Namespace
		}
		if in.Blurb != nil {
			details["blurb"] = change.After.Blurb
		}
		if err := audit.At(s.now()).RecordIn(ctx, tx, actor, id, "project.renamed", change.After.Slug, details); err != nil {
			return err
		}
		if err := project.CompleteRenameIn(ctx, tx, claim); err != nil {
			return err
		}
		out = change.After
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

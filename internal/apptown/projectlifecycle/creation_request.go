package projectlifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

func (s *Service) createRequested(ctx context.Context, in Draft) (*project.Project, error) {
	caller, ok := access.CallerFrom(ctx)
	if !ok || !caller.Verified || caller.Principal.ID.IsZero() {
		return nil, access.ErrPrincipalRequired
	}
	if s.identity == nil {
		return nil, access.ErrAuthorityUnavailable
	}
	if !in.ID.IsZero() {
		return nil, fault.New(fault.Invalid, "a creation request reserves its own project identity")
	}
	normalized, err := normalizeCreation(in)
	if err != nil {
		return nil, err
	}
	var out project.Project
	var releaseNames func()
	defer func() {
		if releaseNames != nil {
			releaseNames()
		}
	}()
	err = s.db.InTx(ctx, func(tx *database.Tx) error {
		claim, err := project.ClaimCreationIn(ctx, tx, s.now(), caller.Principal.ID, in.RequestID)
		if err != nil {
			return err
		}
		if !claim.Fresh {
			hash, err := creationHash(normalized, claim.ResolvedGroup)
			if err != nil {
				return err
			}
			if hash != claim.InputHash {
				return project.ErrRequestReused
			}
			if _, err := (access.Checked{Authority: s.identity, Projects: s.projects}).Require(ctx, access.Permission{Capability: access.CapProjectRead, Resource: access.Resource{Type: "project", ID: claim.ProjectID.String(), ProjectID: claim.ProjectID}}); err != nil {
				return err
			}
			if err := project.RefuseUnlessActiveIn(ctx, tx, claim.ProjectID); err != nil {
				return err
			}
			if _, err := project.ByIDIn(ctx, tx, claim.ProjectID); err != nil {
				return err
			}
			out = claim.Result
			return nil
		}
		// A completed request is resolved before any fresh-only catalog or default-group work.
		if err := identity.RequireEnabledIn(ctx, tx, caller.Principal); err != nil {
			return err
		}
		current := caller.Principal
		current.Name, err = identity.PersonNameIn(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		if current.Name == "" {
			return access.ErrPrincipalRequired
		}
		group, err := s.identity.OwningGroupFor(ctx, current, normalized.Group)
		if err != nil {
			return err
		}
		if _, err := s.identity.Require(ctx, caller.Principal, access.Permission{Capability: access.CapProjectCreate, Resource: access.Resource{Type: "group", ID: group}}); err != nil {
			return err
		}
		if len(normalized.Packs) > 0 {
			if _, err := s.OfferedPacks(ctx, normalized.Packs); err != nil {
				return err
			}
		}
		if normalized.Setup != nil && normalized.Setup.Origin == "url" {
			if _, err := (scm.URLPolicy{AllowPrivate: s.seeding.AllowPrivate, Resolve: s.resolve}).Check(ctx, normalized.Setup.SourceURL); err != nil {
				return err
			}
		}
		receipt, err := s.adoption.UnionIn(ctx, tx, ids.Nil, nil)
		if err != nil {
			return err
		}
		hash, err := creationHash(normalized, group)
		if err != nil {
			return err
		}
		// Keep account removal/name reuse from changing who receives ownership after admission.
		if err := identity.LockUsersIn(ctx, tx); err != nil {
			return err
		}
		if err := identity.RequireEnabledIn(ctx, tx, caller.Principal); err != nil {
			return err
		}
		chosen, err := claimCreationAddress(ctx, tx, claim.ProjectID, normalized.Slug, normalized.AutomaticAddress)
		if err != nil {
			return err
		}
		releaseNames, err = s.reserveNames(chosen)
		if err != nil {
			return err
		}
		command := normalized
		command.ID = claim.ProjectID
		command.Slug = chosen
		command.Actor = caller.Receipt
		if command.Actor == "" {
			command.Actor = caller.Principal.Name
		}
		out, err = s.createIn(ctx, tx, command, group, receipt, s.now())
		if err != nil {
			return err
		}
		return project.CompleteCreationIn(ctx, tx, claim, hash, group)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func claimCreationAddress(ctx context.Context, tx *database.Tx, id ids.ID, proposed string, automatic bool) (string, error) {
	if automatic && len(proposed) > 63 {
		proposed = strings.TrimRight(proposed[:63], "-")
	}
	if err := project.AddressFreeIn(ctx, tx, id, proposed); err == nil {
		return proposed, nil
	} else if !automatic || !errors.Is(err, project.ErrSlugTaken) {
		return "", err
	}
	// The full reserved UUID makes the fallback independent of another
	// request's selected suffix.
	base := proposed
	if len(base) > 26 {
		base = strings.TrimRight(base[:26], "-")
	}
	candidate := base + "-" + id.String()
	if err := project.AddressFreeIn(ctx, tx, id, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

func normalizeCreation(in Draft) (Draft, error) {
	if !project.ValidSlug(in.Slug) {
		return Draft{}, errInvalidSlug
	}
	in.Group = strings.TrimSpace(in.Group)
	if in.Setup == nil {
		return in, nil
	}
	setup := *in.Setup
	setup.SourceURL = strings.TrimSpace(setup.SourceURL)
	setup.SourceBranch = strings.TrimSpace(setup.SourceBranch)
	setup.Services = slices.Clone(setup.Services)
	for i := range setup.Services {
		sv := &setup.Services[i]
		if sv.Capability == "" {
			return Draft{}, fault.New(fault.Invalid, "a selected service needs its capability")
		}
		params := map[string]string{}
		for k, v := range sv.Params {
			params[k] = v
		}
		sv.Params = params
	}
	slices.SortFunc(setup.Services, func(a, b CreationService) int { return strings.Compare(a.Capability, b.Capability) })
	for i := 1; i < len(setup.Services); i++ {
		if setup.Services[i-1].Capability == setup.Services[i].Capability {
			return Draft{}, fault.New(fault.Invalid, "a service capability was selected twice")
		}
	}
	switch setup.Origin {
	case "empty":
		if setup.SourceURL != "" || setup.SourceBranch != "" || setup.CredentialSecret != "" || setup.BundleSHA256 != "" {
			return Draft{}, fault.New(fault.Invalid, "an empty repository has no import source")
		}
	case "url":
		if setup.SourceURL == "" || setup.BundleSHA256 != "" {
			return Draft{}, fault.New(fault.Invalid, "a URL import needs its source URL and no bundle digest")
		}
	case "bundle":
		digest, err := hex.DecodeString(setup.BundleSHA256)
		if err != nil || len(digest) != sha256.Size || setup.BundleSHA256 != strings.ToLower(setup.BundleSHA256) || setup.SourceURL != "" || setup.CredentialSecret != "" {
			return Draft{}, fault.New(fault.Invalid, "a bundle import needs its SHA-256 digest and no URL credential")
		}
	default:
		return Draft{}, fault.New(fault.Invalid, "choose an empty, URL or bundle repository")
	}
	if setup.Upstream.Enabled && (setup.Upstream.AttachmentRequestID.IsZero() || setup.Upstream.RepositoryName == "" || setup.Upstream.NamespaceID < 0) {
		return Draft{}, fault.New(fault.Invalid, "an upstream choice needs its repository name and attachment request identity")
	}
	in.Setup = &setup
	return in, nil
}

func creationHash(in Draft, group string) (string, error) {
	// Request identity and attribution select the record; they are not editable input.
	raw, err := json.Marshal(struct {
		Slug, Name, Namespace, Blurb, Icon, AskedGroup, ResolvedGroup string
		Packs                                                         []string
		AutomaticAddress                                              bool
		Setup                                                         *CreationSetup
	}{in.Slug, in.Name, in.Namespace, in.Blurb, in.Icon, in.Group, group, in.Packs, in.AutomaticAddress, in.Setup})
	if err != nil {
		return "", fmt.Errorf("encode project creation intent: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

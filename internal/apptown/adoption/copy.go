package adoption

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/adoption/internal/adoptionsql"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/ids"
)

// ErrNotCopied is a project's adoption that cannot be given to another
// project exactly.
var ErrNotCopied = errors.New("the project's way of working cannot be copied exactly")

// PrepareCopy prepares for dst, a project just created, exactly the
// adoption src is on now: the same pack memberships at the same versions,
// the same blocks (versions and disabled ones included), the same lanes at
// the same workflow versions, the same installed policies with their
// configurations, and the same extensions. It is applied as a pack
// application (OperationApplyPack). The candidate is checked against src's
// receipt: anything that would differ is ErrNotCopied, never an
// approximation. It is how an eval's throwaway copy runs what its source
// project runs now (design/subsystems/conformance.md).
func (s *Service) PrepareCopy(ctx context.Context, dst, src ids.ID) (*Candidate, error) {
	return s.PrepareCopyWith(ctx, dst, src, nil)
}

// PrepareCopyWith is PrepareCopy of the adoption src would have with these
// pack picks: exactly the candidate Prepare describes for src (its own
// additions, disabled blocks and lanes kept), given to dst. Nil picks are
// what src is on now. It is how a pack-version eval's variant copy runs
// the candidate version: anything that would differ from that candidate is ErrNotCopied.
func (s *Service) PrepareCopyWith(ctx context.Context, dst, src ids.ID, picks []PackPick) (*Candidate, error) {
	var copyOf *CopySource
	if picks == nil {
		copyOf = &CopySource{Project: src}
	}
	target, picks, err := s.CopyTarget(ctx, src, picks)
	if err != nil {
		return nil, err
	}
	return s.prepareCopy(ctx, dst, src, target, picks, copyOf)
}

// PrepareCopyAt is PrepareCopy of the receipt src recorded at revision:
// how an eval's copy starts where an adoption Change it re-runs started.
// It reproduces that receipt only. Pack memberships are not recorded per
// revision, so the copy's are src's picks now. Policies and extensions are
// installed as src has them now; a receipt that recorded others cannot be
// copied (ErrNotCopied).
func (s *Service) PrepareCopyAt(ctx context.Context, dst, src ids.ID, revision int64) (*Candidate, error) {
	at, err := receiptAtIn(ctx, s.db, src, revision)
	if err != nil {
		return nil, err
	}
	picks, err := s.currentPicks(ctx, src)
	if err != nil {
		return nil, err
	}
	return s.prepareCopy(ctx, dst, src, at.Receipt, picks, &CopySource{Project: src, Revision: revision})
}

// prepareCopy gives dst exactly target, with src's installed policies and
// target's extensions, as a pack application of picks.
func (s *Service) prepareCopy(ctx context.Context, dst, src ids.ID, target Receipt, picks []PackPick, copyOf *CopySource) (*Candidate, error) {
	c, err := s.Prepare(ctx, dst, Request{Packs: picks, Additions: []string{}})
	if err != nil {
		return nil, err
	}
	if c.Packs == nil {
		c.Packs = []PackPick{}
	}
	c.CopyOf = copyOf
	c.Receipt.Resolver, c.Receipt.Packs = target.Resolver, slices.Clone(target.Packs)
	c.Receipt.Guidance = slices.Clone(target.Guidance)
	c.Receipt.Workflows = slices.Clone(target.Workflows)
	c.Receipt.Extensions = []string{}
	policies, err := ReadProjectPolicies(ctx, s.db, src)
	if err != nil {
		return nil, err
	}
	c, err = preparePackInstallation(c, policies, wow.PackSetup{Extensions: slices.Clone(target.Extensions)})
	if err != nil {
		return nil, err
	}
	if c.Digest != Digest(target) {
		return nil, fmt.Errorf("%w: the copy's adoption would differ from the source's", ErrNotCopied)
	}
	return c, nil
}

// PrepareCopyUplift is the target of an adoption Change in dst, an eval's
// copy: exactly the receipt a copy of src would get with these picks (nil:
// src's own now), described against what dst has adopted, so its new
// checks are the ones the move brings. It is a selection: the policies and
// extensions it records must be the ones dst has installed, since an
// adoption Change installs none (ErrNotCopied otherwise).
func (s *Service) PrepareCopyUplift(ctx context.Context, dst, src ids.ID, picks []PackPick) (*Candidate, error) {
	var copyOf *CopySource
	if picks == nil {
		copyOf = &CopySource{Project: src}
	}
	target, picks, err := s.CopyTarget(ctx, src, picks)
	if err != nil {
		return nil, err
	}
	c, err := s.Prepare(ctx, dst, Request{Packs: picks, Additions: []string{}})
	if err != nil {
		return nil, err
	}
	if c.Packs == nil {
		c.Packs = []PackPick{}
	}
	c.CopyOf = copyOf
	c.Receipt = cloneReceipt(target)
	c.Digest = Digest(c.Receipt)
	current, err := s.CurrentReceipt(ctx, dst)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(current.Receipt.Policies, target.Policies) || !slices.Equal(current.Receipt.Extensions, target.Extensions) {
		return nil, fmt.Errorf("%w: the move changes installed policies or extensions, which an adoption Change does not install", ErrNotCopied)
	}
	memberships, err := ProjectPacksIn(ctx, s.db, dst)
	if err != nil {
		return nil, err
	}
	view, err := candidatePlacement(current, memberships)
	if err != nil {
		return nil, err
	}
	if err := (candidateBuilder{ways: s.deps.Ways}).describe(ctx, current, view.Carried, c); err != nil {
		return nil, err
	}
	return c, nil
}

// receiptAtIn is the receipt projectID recorded at revision (above zero).
func receiptAtIn(ctx context.Context, q database.Queryer, projectID ids.ID, revision int64) (*RecordedRevision, error) {
	if revision <= 0 {
		return nil, fmt.Errorf("%w: revision %d", ErrRevisionUnavailable, revision)
	}
	r, err := adoptionsql.New(q.SQL()).AdoptionAtRevision(ctx, adoptionsql.AdoptionAtRevisionParams{ProjectID: projectID, Revision: revision})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: revision %d", ErrRevisionUnavailable, revision)
	}
	if err != nil {
		return nil, err
	}
	return adoptionRecord(r)
}

// ReceiptAt is the receipt projectID recorded at revision: what an eval
// re-running an adoption Change starts its copy from.
func (s *Service) ReceiptAt(ctx context.Context, projectID ids.ID, revision int64) (*RecordedRevision, error) {
	return receiptAtIn(ctx, s.db, projectID, revision)
}

// CopySource names the recorded adoption a copy reproduces: the source
// project's receipt at Revision, or its current one when Revision is 0.
type CopySource struct {
	Project  ids.ID `json:"project"`
	Revision int64  `json:"revision,omitempty"`
}

// CopyPurpose is Project's answer to whether a project is an eval's copy.
type CopyPurpose interface {
	IsEvalCopyIn(ctx context.Context, q database.Queryer, projectID ids.ID) (bool, error)
}

// ErrNotACopy refuses a copy's candidate for a project that is not an eval
// copy.
var ErrNotACopy = errors.New("only an eval's copy takes another project's way of working as it is")

// validateCopyIn is how a copy's candidate is checked, in place of its
// packs' pins: a copy reproduces what its source recorded, and the source's
// receipt may no longer match its memberships' pins (AdvanceToLatest moves
// blocks and lanes and leaves memberships alone, on purpose). It must go
// to an eval copy, carry exactly the source's receipt (its current one, or
// the one it recorded at the revision named), and the source's current
// pack picks. Everything else in validateCandidateIn is still asked. Read
// through q, the adopting transaction.
func (s *Service) validateCopyIn(ctx context.Context, q database.Queryer, projectID ids.ID, c Candidate) error {
	if s.deps.Copies == nil {
		return ErrNotACopy
	}
	copy, err := s.deps.Copies.IsEvalCopyIn(ctx, q, projectID)
	if err != nil {
		return err
	}
	if !copy {
		return ErrNotACopy
	}
	var source *RecordedRevision
	if c.CopyOf.Revision != 0 {
		source, err = receiptAtIn(ctx, q, c.CopyOf.Project, c.CopyOf.Revision)
	} else {
		source, err = CurrentReceiptIn(ctx, q, c.CopyOf.Project)
	}
	if err != nil {
		return fmt.Errorf("reading the source project's adoption: %w", err)
	}
	if Digest(source.Receipt) != c.Digest {
		return fmt.Errorf("%w: the source's way of working changed since the copy was prepared", ErrNotCopied)
	}
	picks, err := currentPicksIn(ctx, q, c.CopyOf.Project)
	if err != nil {
		return err
	}
	if !slices.Equal(picks, c.Packs) {
		return fmt.Errorf("%w: the source's packs changed since the copy was prepared", ErrNotCopied)
	}
	return nil
}

// CopyTarget is the receipt a copy of src would get with these picks, and
// the picks in the order they are adopted: src's current receipt for nil
// picks, else the candidate Prepare describes for src. A candidate src
// could not adopt is an error naming why.
func (s *Service) CopyTarget(ctx context.Context, src ids.ID, picks []PackPick) (Receipt, []PackPick, error) {
	if picks == nil {
		source, err := s.CurrentReceipt(ctx, src)
		if err != nil {
			return Receipt{}, nil, fmt.Errorf("reading the source project's adoption: %w", err)
		}
		if picks, err = s.currentPicks(ctx, src); err != nil {
			return Receipt{}, nil, err
		}
		return source.Receipt, picks, nil
	}
	c, err := s.Prepare(ctx, src, Request{Packs: picks})
	if err != nil {
		return Receipt{}, nil, err
	}
	if len(c.Problems) > 0 {
		return Receipt{}, nil, fmt.Errorf("%w: %s", ErrNotCopied, strings.Join(c.Problems, "; "))
	}
	return c.Receipt, picks, nil
}

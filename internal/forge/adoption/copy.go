package adoption

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"flokkit.io/forge/internal/forge/wow"
	"flokkit.io/forge/internal/ids"
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
	c.Receipt.Resolver, c.Receipt.Pack = target.Resolver, target.Pack
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

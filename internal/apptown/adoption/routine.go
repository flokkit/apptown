package adoption

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// RoutinePublications is the publication owner's consent check, in the same
// transaction as the project inventory. Callers cannot supply classification IDs.
type RoutinePublications interface {
	CheckTransitionsIn(context.Context, *database.Tx, []wow.RoutinePublicationTransition) ([]wow.RoutinePublicationClassification, error)
}

type RoutineConsentRef struct {
	ID     ids.ID `json:"id"`
	Digest string `json:"digest"`
	Shown  string `json:"shown"`
}

type RoutineTarget struct {
	CurrentID       ids.ID              `json:"currentId"`
	TargetID        ids.ID              `json:"targetId"`
	CandidateDigest string              `json:"candidateDigest"`
	Classifications []RoutineConsentRef `json:"classifications"`
}

// CheckRoutineTargetIn checks the actual immutable Change target against the
// complete current adoption. The caller owns project/run/rule locks before this
// call; publication consent locks remain held until its transaction ends.
func (s *Service) CheckRoutineTargetIn(ctx context.Context, tx *database.Tx, projectID, changeID, targetID ids.ID, expectedRevision int64, candidateJSON []byte) (RoutineTarget, error) {
	var out RoutineTarget
	if s.deps.RoutinePublications == nil {
		return out, fault.New(fault.Unavailable, "routine publication consent is unavailable")
	}
	current, err := CurrentReceiptIn(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	if current.Revision != expectedRevision {
		return out, fault.New(fault.Conflict, "the adoption changed after the target was prepared")
	}
	target, err := ProjectAdoptionIn(ctx, tx, projectID, targetID)
	if err != nil {
		return out, err
	}
	var candidate Candidate
	if err = json.Unmarshal(candidateJSON, &candidate); err != nil {
		return out, err
	}
	candidate.ProjectID = projectID
	if err = target.checkTarget(projectID, changeID, expectedRevision, candidate); err != nil {
		return out, err
	}
	if candidate.CopyOf != nil || len(candidate.Installation.Policies) != 0 || len(candidate.Installation.Extensions) != 0 {
		return out, fault.New(fault.Conflict, "routine adoption cannot add authority, installations or copied state")
	}
	if err = s.validateCandidateIn(ctx, tx, projectID, candidate); err != nil {
		return out, err
	}
	if err = s.validateTargetChecksIn(ctx, tx, projectID, candidate); err != nil {
		return out, err
	}
	memberships, err := ProjectPacksIn(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	previousPicks := make([]PackPick, 0, len(memberships))
	for _, membership := range memberships {
		previousPicks = append(previousPicks, PackPick{Pack: membership.Pack, Version: membership.Version})
	}
	beforeRequirements, err := requiredCapabilitiesIn(ctx, tx, Candidate{Packs: previousPicks})
	if err != nil {
		return out, err
	}
	afterRequirements, err := requiredCapabilitiesIn(ctx, tx, candidate)
	if err != nil {
		return out, err
	}
	if !slices.Equal(beforeRequirements, afterRequirements) {
		return out, fault.New(fault.Conflict, "routine adoption cannot change capability requirements")
	}
	if err = s.requireCapabilities(ctx, tx, projectID, afterRequirements); err != nil {
		return out, err
	}
	view, err := candidatePlacement(current, memberships)
	if err != nil {
		return out, err
	}
	additions := make([]string, 0, len(view.Additions))
	for _, block := range view.Additions {
		additions = append(additions, block.Name)
	}
	if !slices.Equal(additions, candidate.Additions) {
		return out, fault.New(fault.Conflict, "routine adoption cannot change additional guidance membership")
	}
	moves, err := routineAdoptionMoves(current.Receipt, candidate.Receipt, memberships, candidate.Packs)
	if err != nil {
		return out, err
	}
	// Pack moves carry identity from the authoritative current membership names.
	for i := range memberships {
		before, after := memberships[i], candidate.Packs[i]
		if before.Version == after.Version {
			continue
		}
		pack, e := wow.PackByNameIn(ctx, tx, before.Pack)
		if e != nil {
			return out, e
		}
		moves = append(moves, wow.RoutinePublicationTransition{Kind: "pack", ID: pack.ID, From: before.Version, To: after.Version})
	}
	if len(moves) == 0 {
		return out, fault.New(fault.Conflict, "the adoption has no publication transition")
	}
	classifications, err := s.deps.RoutinePublications.CheckTransitionsIn(ctx, tx, moves)
	if err != nil {
		return out, err
	}
	material, err := json.Marshal(candidate)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(material)
	refs := make([]RoutineConsentRef, 0, len(classifications))
	for _, record := range classifications {
		refs = append(refs, RoutineConsentRef{ID: record.ID, Digest: record.Question.Digest, Shown: record.Question.Shown})
	}
	return RoutineTarget{CurrentID: current.ID, TargetID: target.ID, CandidateDigest: hex.EncodeToString(digest[:]), Classifications: refs}, nil
}

// routineAdoptionMoves compares every receipt field and membership. Only exact
// forward versions may differ; order, disabled state, tier and all other policy
// inputs remain part of the comparison rather than display-only summaries.
func routineAdoptionMoves(before, after Receipt, memberships []ProjectPackRecord, picks []PackPick) ([]wow.RoutinePublicationTransition, error) {
	refuse := func() ([]wow.RoutinePublicationTransition, error) {
		return nil, fault.New(fault.Conflict, "routine adoption must preserve membership, checks, workflows, policies and authority")
	}
	if len(memberships) != len(picks) || len(before.Guidance) != len(after.Guidance) {
		return refuse()
	}
	for i, old := range memberships {
		next := picks[i]
		if old.Pack != next.Pack || old.Version <= 0 || next.Version < old.Version {
			return refuse()
		}
	}
	adjusted := after
	adjusted.Guidance = slices.Clone(after.Guidance)
	var moves []wow.RoutinePublicationTransition
	for i, old := range before.Guidance {
		next := after.Guidance[i]
		if old.Version <= 0 || next.Version < old.Version || old.BlockID.IsZero() {
			return refuse()
		}
		if old.Version != next.Version {
			moves = append(moves, wow.RoutinePublicationTransition{Kind: "block", ID: old.BlockID, From: int64(old.Version), To: int64(next.Version)})
		}
		adjusted.Guidance[i].Version = old.Version
	}
	// Stored receipts and owner preparation use both null and [] for an empty
	// collection. They express the same selection; nonempty material and order
	// still compare exactly.
	if len(before.Workflows) == 0 && len(adjusted.Workflows) == 0 {
		before.Workflows = nil
		adjusted.Workflows = nil
	}
	if len(before.Policies) == 0 && len(adjusted.Policies) == 0 {
		before.Policies = nil
		adjusted.Policies = nil
	}
	if len(before.Extensions) == 0 && len(adjusted.Extensions) == 0 {
		before.Extensions = nil
		adjusted.Extensions = nil
	}
	if !reflect.DeepEqual(before, adjusted) {
		return refuse()
	}
	return moves, nil
}

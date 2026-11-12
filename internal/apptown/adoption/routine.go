package adoption

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

type RoutineTarget struct {
	CurrentID       ids.ID `json:"currentId"`
	TargetID        ids.ID `json:"targetId"`
	CandidateDigest string `json:"candidateDigest"`
}

// CheckRoutineTargetIn checks the actual immutable Change target against the
// complete current adoption. The caller owns project/run/rule locks before this
// call. Published additions and changed checks need no interval classification.
func (s *Service) CheckRoutineTargetIn(ctx context.Context, tx *database.Tx, projectID, changeID, targetID ids.ID, expectedRevision int64, candidateJSON []byte) (RoutineTarget, error) {
	var out RoutineTarget
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
	required, err := requiredCapabilitiesIn(ctx, tx, candidate)
	if err != nil {
		return out, err
	}
	if err := s.requireCapabilities(ctx, tx, projectID, required); err != nil {
		return out, err
	}
	fingerprint, err := candidate.Fingerprint()
	if err != nil {
		return out, err
	}
	return RoutineTarget{CurrentID: current.ID, TargetID: target.ID, CandidateDigest: fingerprint}, nil
}

// Fingerprint includes membership versions and requirements as well as the
// receipt. Target IDs allocated by individual Changes are not candidate data.
func (c Candidate) Fingerprint() (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

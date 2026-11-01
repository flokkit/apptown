package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/database"
)

type mergeRequest struct {
	Identity scm.RepositoryIdentity
	IID      int
	Project  string
}

// Merge inputs are a database observation, not a bag of callbacks into App.
// Paths derive from this snapshot; adapters cannot silently switch to another
// branch, workspace or merge provider after a second lookup fails.
type MergeSource struct {
	Source
	MR mergeRequest
	// Landing is the pinned candidate and target; empty for a run admitted
	// before candidates were pinned. PinRequired marks a landing admitted
	// under pinning: without a candidate it is refused, never merged the
	// old way.
	Landing     LandingCandidate
	PinRequired bool
}

func ReadMergeSourceIn(ctx context.Context, db database.Queryer, run RunRef) (MergeSource, error) {
	row, err := readSourceRow(ctx, db, run)
	if err != nil {
		return MergeSource{}, err
	}
	source, err := sourceRecord(run, row)
	if err != nil {
		return MergeSource{}, err
	}
	merged := MergeSource{Source: source, PinRequired: row.LandingPinRequired}
	if len(row.LandingCandidate) > 0 && string(row.LandingCandidate) != "null" {
		if err := json.Unmarshal(row.LandingCandidate, &merged.Landing); err != nil {
			return MergeSource{}, fmt.Errorf("run %d has an unreadable landing candidate: %w", run.ID, err)
		}
	}
	if row.HasMr {
		number, ok := strings.CutPrefix(row.MrName, "!")
		iid, err := strconv.Atoi(number)
		if !ok || err != nil || iid <= 0 || strconv.Itoa(iid) != number || strings.TrimSpace(row.MrProject) == "" || !source.RepoBacked() {
			return MergeSource{}, fmt.Errorf("run %d has an invalid recorded merge request", run.ID)
		}
		identity, err := scm.DecodeRepositoryIdentity(row.MrIdentity)
		if err != nil {
			return MergeSource{}, err
		}
		if identity != nil && identity.ProjectID != source.ProjectID {
			return MergeSource{}, fmt.Errorf("merge request identity belongs to a different project")
		}
		merged.MR = mergeRequest{IID: iid, Project: row.MrProject}
		if identity != nil {
			merged.MR.Identity = *identity
		}
	}
	return merged, nil
}

func (s MergeSource) UpliftRefusal() string {
	if s.ChangeType == "uplift" && s.ExpectedRevision != s.AdoptionRevision {
		return fmt.Sprintf("the project's way of working moved from revision %d to %d while this change was worked; prepare the adoption again before landing", s.ExpectedRevision, s.AdoptionRevision)
	}
	return ""
}

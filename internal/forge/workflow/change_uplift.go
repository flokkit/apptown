package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow/internal/workflowsql"
)

// TargetRecorder is adoption's named participant. It joins Work's project-first
// transaction, records a target for this exact Change, and returns its identity.
// Candidate is an opaque adoption-owned document, never interpreted as a map by Work.
type TargetRecorder interface {
	RecordTargetIn(context.Context, *database.Tx, TargetChange) (RecordedTarget, error)
}
type TargetChange struct {
	ProjectID, ChangeID ids.ID
	Actor               string
}
type RecordedTarget struct {
	ID        ids.ID
	Candidate json.RawMessage
}
type Uplift struct {
	ProjectID        ids.ID
	ExpectedRevision int64
	Acceptance       Acceptance
	Words            []string
	Target           TargetRecorder
	// Basis is what the decision to open it was taken on, said in one line
	// of the brief (a what-if); empty when none was named.
	Basis string
}

func (s changeStore) openUplift(ctx context.Context, in Uplift, who string) (*Change, error) {
	now := s.now()
	if in.ProjectID.IsZero() || in.ExpectedRevision < 0 || in.Acceptance.TargetDigest == "" || len(in.Words) == 0 || in.Target == nil || strings.TrimSpace(who) == "" {
		return nil, fmt.Errorf("invalid uplift admission")
	}
	return database.Transact(ctx, s.db, func(tx *database.Tx) (*Change, error) {
		project, err := workflowsql.New(tx.SQL()).ProjectForUpdate(ctx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		if err := refuseUnlessActive(ctx, tx, in.ProjectID); err != nil {
			return nil, err
		}
		current := CreatedChange{Slug: project.Slug, DefaultBranch: project.DefaultBranch}
		metadata, err := adoption.CurrentIn(ctx, tx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		revision := metadata.Revision
		if revision != in.ExpectedRevision {
			return nil, fmt.Errorf("project adoption changed from revision %d to %d; prepare again", in.ExpectedRevision, revision)
		}
		open, err := workflowsql.New(tx.SQL()).HasOpenUplift(ctx, workflowsql.HasOpenUpliftParams{ProjectID: in.ProjectID, State: ChangeInProgress.String()})
		if err != nil {
			return nil, err
		}
		if open {
			return nil, fmt.Errorf("an adoption change is already open; land or abandon it first")
		}
		title, body := upliftBrief(in)
		created, err := insertChangeIn(ctx, tx, ChangeDraft{ProjectID: in.ProjectID, Type: "uplift", Title: title, Body: body, Acceptance: &in.Acceptance}, who, current, changeOrigin{expectedRevision: in.ExpectedRevision}, now)
		if err != nil {
			return nil, err
		}
		target, err := in.Target.RecordTargetIn(ctx, tx, TargetChange{in.ProjectID, created.Change.ID, who})
		if err != nil {
			return nil, err
		}
		if target.ID.IsZero() || !json.Valid(target.Candidate) {
			return nil, fmt.Errorf("adoption target was not recorded")
		}
		// Validate the participant's returned identity before attaching it. Target rows
		// are immutable facts, scoped to this project and the negative Change revision.
		owned, err := adoption.TargetOwnedIn(ctx, tx, in.ProjectID, created.Change.ID, target.ID, in.Acceptance.TargetDigest)
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, fmt.Errorf("adoption target is not owned by this Change")
		}
		n, err := workflowsql.New(tx.SQL()).AttachUpliftTarget(ctx, workflowsql.AttachUpliftTargetParams{TargetAdoptionID: target.ID, TargetCandidateJson: target.Candidate, ID: created.Change.ID, ProjectID: in.ProjectID})
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("Change target was not attached")
		}
		receipt := struct {
			Target string `json:"target"`
			Checks int    `json:"checks"`
		}{in.Acceptance.TargetDigest, len(in.Acceptance.Checks)}
		if err := audit.At(now).RecordIn(ctx, tx, who, in.ProjectID, "wow.uplift.opened", fmt.Sprintf("%d", created.Change.ID), receipt); err != nil {
			return nil, err
		}
		return readChange(ctx, tx, created.Change.ID)
	})
}

func upliftBrief(in Uplift) (string, string) {
	var lines []string
	for _, ch := range in.Acceptance.Checks {
		line := "  - " + ch.Block + " · " + ch.Check
		// The check's own expectation, as the remediation brief gives it:
		// listed by name alone, the agent and the reviewer judged the name
		// (soak 34: a tool's working files read as app data).
		if e := strings.TrimSpace(ch.Expects); e != "" {
			line += ": " + e
		}
		lines = append(lines, line)
	}
	title := "Adopt the way of working: " + strings.Join(in.Words, ", ")
	basis := ""
	if in.Basis != "" {
		basis = "\n" + in.Basis + "\n"
	}
	body := fmt.Sprintf(`Bring this app onto: %s.
%s
WHAT MUST HOLD — every check below re-evaluates to CONFORMS against your change's tree,
judged outright (they are what the new way of working brings):
%s

NOTHING TO CHANGE is a correct result: when every check above already holds on the base,
the right change is empty. Say so in your report, with the evidence for each check, and add
nothing (no needs, parts, endpoints or features) to give the change something to do.

ACCEPTANCE: the ordinary gates still pass (tests, boot/preview, review) — do not satisfy a
check by deleting a feature or weakening the app; you did not edit any check definition.

SCOPE: this Change adopts the way of working above and nothing else. Findings outside the
list that already fail on main are not yours here.

When this Change lands, the project's way of working moves to the target — unless the
project moved on meanwhile, in which case the landing is refused and the change is prepared
again. Work in this Change's workspace and land through the ordinary gates.`, strings.Join(in.Words, ", "), basis, strings.Join(lines, "\n"))
	return title, body
}

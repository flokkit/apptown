package conformance

import (
	"context"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/fault"
)

var ErrOriginsChanged = fault.New(fault.Conflict, "saved remediation findings changed or were cleared; select the findings again")

// ValidateRemediationOriginsIn participates in remediation admission. The caller
// must already hold its admission transaction (including the new job row).
// Lock project roots before verdicts, preserving the normal mutation order and
// excluding project deletion. No Git/evaluation or reconstructed evidence occurs
// inside this transaction. Missing/altered origins require an explicit new request.
func ValidateRemediationOriginsIn(ctx context.Context, tx *database.Tx, findings []Observation) error {
	return validateRemediationOrigins(ctx, tx, findings)
}

func validateRemediationOrigins(ctx context.Context, db database.Queryer, findings []Observation) error {
	idList := make([]ids.ID, 0, len(findings))
	projects := make([]ids.ID, 0, len(findings))
	seen := map[ids.ID]bool{}
	for _, finding := range findings {
		if finding.Record == nil || finding.Record.ID.IsZero() {
			return ErrOriginsChanged
		}
		idList = append(idList, finding.Record.ID)
		if !seen[finding.Record.ProjectID] {
			projects = append(projects, finding.Record.ProjectID)
			seen[finding.Record.ProjectID] = true
		}
	}
	locked, err := queries(db).LockProjectsForOrigins(ctx, projects)
	if err != nil {
		return err
	}
	if len(locked) != len(projects) {
		return ErrOriginsChanged
	}
	rows, err := queries(db).VerdictsByIDsForShare(ctx, idList)
	if err != nil {
		return err
	}
	records, err := verdicts(rows)
	if err != nil {
		return err
	}
	byID := make(map[ids.ID]Verdict, len(records))
	var confirmable []ids.ID
	for _, record := range records {
		byID[record.ID] = record
		if record.Confirmable() {
			confirmable = append(confirmable, record.ID)
		}
	}
	confirmations, err := readAdjudications(ctx, db, confirmable)
	if err != nil {
		return err
	}
	for _, finding := range findings {
		saved, ok := byID[finding.Record.ID]
		if !ok || saved != *finding.Record || finding.CheckRef.Identity() != saved.Reference().Identity() {
			return ErrOriginsChanged
		}
		// Preserve the current interpretation of an expired exception, but do
		// not turn a conforming or unassessed saved result into a finding.
		if (saved.State != DoesNotConform && saved.State != Excepted) || LaneOf(finding, confirmations) != LaneFindings {
			return ErrOriginsChanged
		}
	}
	return nil
}

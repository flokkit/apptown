package audit

import (
	"context"
	"encoding/json"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit/internal/auditsql"
)

// ExportProjectIn reads a project's ledger lines verbatim, for moving the
// project to another AppTown, where they are history witnessed here, not
// there.
func ExportProjectIn(ctx context.Context, q database.Queryer, projectID ids.ID) ([]json.RawMessage, error) {
	return database.JSONRows(auditsql.New(q.SQL()).ExportProjectLedger(ctx, projectID))
}

// Moving names what moves with a project: changes, runs and chats.
type Moving struct {
	Changes, Runs, Chats []ids.ID
}

// projectKinds are the receipt kinds about the project itself: its
// settings, adoption, decisions, secret names and members. A line of one
// is eligible to move when it carries no recognised work reference; one
// that does (an ADR edit naming its change) follows that work. A
// call-site source heuristic keeps the list plausible
// (TestProjectKindsAreWrittenWithoutObviousWork).
var projectKinds = map[string]bool{
	"adoption.drift": true, "adoption.recorded": true,
	"adr.created": true, "adr.superseding": true, "adr.updated": true,
	"guidance.manifest.saved": true, "guidance.platform.adopted": true,
	"integration.attached": true, "integration.detached": true,
	"model-profile.set": true, "model-slot.set": true,
	"pack.applied": true, "pack.pulled": true, "policy.installed": true, "policy.removed": true,
	"project.approval-policy": true, "project.copied": true, "project.created": true,
	"project.extension.enabled": true, "project.extension.disabled": true,
	"project.member-added": true, "project.member-removed": true, "project.release-domain": true,
	"project.remote.attached": true, "project.renamed": true, "project.repo.initialized": true,
	"project.repo.seeded": true, "project.services-seed": true, "project.toolchain.seeded": true,
	"project.transferred": true, "registry.project-created": true,
	"secret.bound": true, "secret.unbound": true, "secret.profile-set": true, "secret.revealed": true, "secret.generated": true,
}

// LedgerSelection is what Select decided: the lines that move, and how
// many stay, by kind.
type LedgerSelection struct {
	Lines  []json.RawMessage
	Stayed map[string]int
}

// Select keeps the ledger lines that move with a project, verbatim, and
// is safe by default: a line moves only when its kind is about the project
// itself (projectKinds) and it carries no recognised work reference, or
// when it carries recognised references and all of them move. Every other
// line stays, counted by kind, so a line whose only reference is a shape no
// rule reads stays. The accepted limit: a line with a recognised moving
// reference and another, unread reference to work that stays (sendback's
// sourceRunId, an abandonment's runs list) moves.
func Select(lines []json.RawMessage, move Moving) (LedgerSelection, error) {
	moving := map[string]map[ids.ID]bool{RefChange: {}, RefRun: {}, RefChat: {}}
	for kind, list := range map[string][]ids.ID{RefChange: move.Changes, RefRun: move.Runs, RefChat: move.Chats} {
		for _, id := range list {
			moving[kind][id] = true
		}
	}
	out := LedgerSelection{Lines: make([]json.RawMessage, 0, len(lines)), Stayed: map[string]int{}}
	for _, raw := range lines {
		var line struct {
			Kind string `json:"kind"`
			Refs []Ref  `json:"refs"`
		}
		if err := json.Unmarshal(raw, &line); err != nil {
			return LedgerSelection{}, err
		}
		moves := len(line.Refs) == 0 && projectKinds[line.Kind]
		if len(line.Refs) > 0 {
			moves = true
			for _, r := range line.Refs {
				// An unresolved reference never counts as moving work.
				moves = moves && r.Unresolved == "" && moving[r.Kind][r.ID]
			}
		}
		if !moves {
			out.Stayed[line.Kind]++
			continue
		}
		out.Lines = append(out.Lines, raw)
	}
	return out, nil
}

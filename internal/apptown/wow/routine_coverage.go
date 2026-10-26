package wow

import (
	"context"
	"encoding/json"

	"github.com/flokkit/apptown/internal/apptown/wow/internal/wowsql"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
	"github.com/flokkit/apptown/internal/ids"
)

// CheckTransitionsIn covers the complete transition inventory supplied by the
// Adoption owner. Pack consent covers its exact nested guidance moves; it never
// covers a different version interval, membership change or unrelated block.
// All selected consent rows are locked together in CheckIn's UUID order.
func (s RoutineTransitions) CheckTransitionsIn(ctx context.Context, tx *database.Tx, transitions []RoutinePublicationTransition) ([]RoutinePublicationClassification, error) {
	if len(transitions) == 0 {
		return nil, fault.New(fault.Conflict, "routine adoption must contain a classified publication transition")
	}
	seen := map[RoutinePublicationTransition]bool{}
	for _, move := range transitions {
		if seen[move] || (move.Kind != "pack" && move.Kind != "block") || move.ID.IsZero() || move.From <= 0 || move.To <= move.From {
			return nil, fault.New(fault.Conflict, "routine adoption contains an unsupported or duplicate transition")
		}
		seen[move] = true
	}
	covered := map[RoutinePublicationTransition]bool{}
	var wanted []ids.ID
	selectConsent := func(move RoutinePublicationTransition) error {
		question, err := routineQuestionIn(ctx, tx, move)
		if err != nil {
			return err
		}
		if !question.Eligible {
			return fault.New(fault.Conflict, question.Reason)
		}
		row, err := wowsql.New(tx.SQL()).ActiveRoutineClassification(ctx, question.Digest)
		if err != nil {
			return database.Missing(err)
		}
		record, err := readRoutineClassification(row)
		if err != nil {
			return err
		}
		if record.Question.Digest != question.Digest || record.Question.Transition != move {
			return fault.New(fault.Conflict, "routine classification does not cover the exact publication")
		}
		wanted = append(wanted, record.ID)
		var before, after routinePublication
		if err = json.Unmarshal(question.Before, &before); err != nil {
			return err
		}
		if err = json.Unmarshal(question.After, &after); err != nil {
			return err
		}
		routineCoveredMoves(before, after, covered)
		return nil
	}
	// Pack membership transitions need their own explicit classification even
	// when another selected pack includes the same publication.
	for _, move := range transitions {
		if move.Kind == "pack" {
			if err := selectConsent(move); err != nil {
				return nil, err
			}
		}
	}
	for _, move := range transitions {
		if move.Kind == "block" && !covered[move] {
			if err := selectConsent(move); err != nil {
				return nil, err
			}
		}
	}
	return s.CheckIn(ctx, tx, wanted)
}

func routineCoveredMoves(before, after routinePublication, covered map[RoutinePublicationTransition]bool) {
	if before.Kind == "block" && before.ID == after.ID && before.Version < after.Version {
		covered[RoutinePublicationTransition{Kind: "block", ID: before.ID, From: before.Version, To: after.Version}] = true
	}
	// Only called after the complete publication comparator established equal
	// ordered component identity. No matching by display name or guessed latest.
	for i := range before.References {
		routineCoveredMoves(before.References[i], after.References[i], covered)
	}
}

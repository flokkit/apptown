package adoption

import (
	"context"
	"github.com/flokkit/apptown/internal/ids"
	"time"

	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
	"github.com/flokkit/apptown/internal/fault"
)

var (
	ErrGuidanceRevisionRequired = fault.New(fault.Invalid, "send the adoption revision the edit started from (expectedRevision)")
	// ErrGuidanceMoved carries a code so a client can tell it from other
	// conflicts and offer the person the current list.
	ErrGuidanceMoved error = guidanceMoved{}
)

type guidanceMoved struct{}

func (guidanceMoved) Error() string {
	return "the project's way of working changed since this edit started; review the current guidance before saving again"
}
func (guidanceMoved) FaultKind() fault.Kind { return fault.Conflict }
func (guidanceMoved) FaultCode() string     { return "settings-moved" }

// ReplaceGuidance replaces a project's guidance imports and records the
// resulting adoption revision in one transaction. expected is the adoption
// revision the edit started from: every change to the way of working
// advances it, so an edit of a list that has moved on (another edit, a pack
// pulled, a lane selected) is refused before anything is written.
func (s *Service) ReplaceGuidance(ctx context.Context, projectID ids.ID, expected *int64, pins []wow.ResolvedPin, reason, by string) (*RecordedRevision, error) {
	if expected == nil {
		return nil, ErrGuidanceRevisionRequired
	}
	return s.Advance(ctx, projectID, AdoptionEdit, reason, by, func(tx *database.Tx, prior *RecordedRevision, now time.Time) (Receipt, error) {
		var current int64
		if prior != nil {
			current = prior.Revision
		}
		if current != *expected {
			return Receipt{}, ErrGuidanceMoved
		}
		if err := PutImportsIn(ctx, tx, projectID, pins, now); err != nil {
			return Receipt{}, err
		}
		return s.UnionIn(ctx, tx, projectID, prior)
	})
}

// GuidanceView is a project's guidance as an editor starts from it: the
// imports and the adoption revision they belong to, read together.
type GuidanceView struct {
	Revision int64
	Text     string
	Hash     string
	Imports  []Import
}

// GuidanceSnapshot reads the imports and the adoption revision in one
// read-only snapshot, so an edit started from it names the revision of the
// list it shows. Read separately, a save landing between them would pair an
// old list with the new revision, and saving that list would pass the guard.
func (s *Service) GuidanceSnapshot(ctx context.Context, projectID ids.ID) (GuidanceView, error) {
	tx, err := s.db.ReadSnapshot(ctx)
	if err != nil {
		return GuidanceView{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var view GuidanceView
	current, err := CurrentIn(ctx, tx, projectID)
	if found, err := database.Lookup(err); err != nil {
		return GuidanceView{}, err
	} else if found {
		view.Revision = current.Revision
	}
	if view.Imports, err = importsIn(ctx, tx, projectID); err != nil {
		return GuidanceView{}, err
	}
	if s.deps.Ways != nil {
		for i, im := range view.Imports {
			view.Imports[i].Withdrawn = s.deps.Ways.Withdrawn(wow.KindGuidance, im.Origin, im.Name)
		}
	}
	if err := tx.Commit(); err != nil {
		return GuidanceView{}, err
	}
	compiled, err := compileGuidance(ctx, s.library.ways, view.Imports, "import ")
	if err != nil {
		return GuidanceView{}, err
	}
	view.Text, view.Hash = compiled.Text, compiled.Hash
	return view, nil
}

// AdvanceToLatest moves a project to the newest version of everything it
// already adopts, as one revision: each guidance import keeps its place and
// disabled choice and is pinned to its block's latest version, and each
// selected workflow resolves to the library's latest. Pack memberships,
// lanes, policies and extensions are unchanged. An adoption already at the
// latest records nothing new (Advance keeps an unchanged receipt).
func (s *Service) AdvanceToLatest(ctx context.Context, projectID ids.ID, reason, by string) (*RecordedRevision, error) {
	return s.Advance(ctx, projectID, AdoptionEdit, reason, by, func(tx *database.Tx, _ *RecordedRevision, now time.Time) (Receipt, error) {
		imports, err := importsIn(ctx, tx, projectID)
		if err != nil {
			return Receipt{}, err
		}
		pins := make([]wow.ResolvedPin, 0, len(imports))
		for _, im := range imports {
			pins = append(pins, wow.ResolvedPin{BlockID: im.BlockID, Name: im.Name, Version: im.LatestVersion, Disabled: im.Disabled})
		}
		if err := PutImportsIn(ctx, tx, projectID, pins, now); err != nil {
			return Receipt{}, err
		}
		// No prior receipt: its workflow pins are what this moves past.
		return s.UnionIn(ctx, tx, projectID, nil)
	})
}

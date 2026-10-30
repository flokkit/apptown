package projectlifecycle

import (
	"context"
	"log"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

// NoteAdoption records an edit-kind revision after a person changed what
// the project is on; failures are logged, never fatal to the edit.
func (s *Service) NoteAdoption(ctx context.Context, projectID ids.ID, reason, by string) {
	p, err := s.projects.ByID(ctx, projectID)
	if err != nil {
		return
	}
	if _, err := s.adoption.RecordCurrent(ctx, p.ID, adoption.AdoptionEdit, reason, by); err != nil {
		log.Printf("adoption: project %d: %v", projectID, err)
	}
}

// SelectLane selects or deselects a lane for by; the selection and the
// adoption revision it makes commit together (adoption.SelectLane).
func (s *Service) SelectLane(ctx context.Context, projectID ids.ID, name string, enabled bool, by string) error {
	return s.adoption.SelectLane(ctx, projectID, name, enabled, by)
}

// ReplaceGuidance saves a person's edit of the guidance list: the imports and
// the adoption revision they make commit together, and only if the way of
// working is still at the revision the edit started from. The manifest audit
// line stays best effort after the write.
func (s *Service) ReplaceGuidance(ctx context.Context, p *project.Project, expected *int64, resolved []wow.ResolvedPin, by string) error {
	if _, err := s.adoption.ReplaceGuidance(ctx, p.ID, expected, resolved, "guidance edited under Settings › Guidance", by); err != nil {
		return err
	}
	if compiled, err := s.adoption.Compile(ctx, p.ID); err == nil {
		hash, imports := compiled.Hash, compiled.Imports
		pins := make([]map[string]any, 0, len(imports))
		for _, im := range imports {
			pins = append(pins, map[string]any{"name": im.Name, "version": im.Version, "disabled": im.Disabled})
		}
		audit.At(s.now()).BestEffort(ctx, s.db, by, p.ID, "guidance.manifest.saved", p.Slug, map[string]any{"hash": hash, "imports": pins})
	}
	return nil
}

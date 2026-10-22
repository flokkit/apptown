// Package changeactions coordinates Change admission and its optional provenance
// across HTTP, MCP and console callers. Work remains the authority for transitions.
package changeactions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/database"
)

type Service struct {
	work *workflow.Service
	db   *database.DB
}

// now is AppTown's clock as Work holds it.
func (s *Service) now() time.Time { return s.work.Now() }

func New(work *workflow.Service, db *database.DB) *Service {
	return &Service{work: work, db: db}
}

// ApplyCatalog applies a catalog change — render its definition, open a
// Change, start its run, record provenance. Shared by the API handler and
// the console actuator (project chats kick off catalog changes as one of
// their sanctioned mutations).
func (s *Service) ApplyCatalog(ctx context.Context, p *project.Project, c *wow.CatalogChange, params map[string]string, who string) (*workflow.Change, *workflow.Run, error) {
	def := c.Definition
	body, err := def.Render(def.Prompt, params)
	if err != nil {
		return nil, nil, err
	}
	title := def.Title
	if title == "" {
		title = c.Name
	}
	if title, err = def.Render(title, params); err != nil {
		return nil, nil, err
	}
	inputs := c.RunInputs(params)
	item, run, err := s.work.CreateAndRun(ctx, workflow.InvokeChange{
		Change:   workflow.ChangeDraft{ProjectID: p.ID, Type: def.Type, Title: title, Body: body},
		Workflow: def.Workflow, Inputs: json.RawMessage(encodedJSON(inputs)),
	})
	if err != nil {
		return item, run, err
	}
	// The run is made: its notice is written though the caller has gone.
	workflow.RecordNoticeOrLog(context.WithoutCancel(ctx), s.db, run.ID, "catalog.applied", map[string]any{
		"catalog": c.Name, "release": wow.CatalogRelease(),
		"params": params, "by": who}, s.now())
	return item, run, nil
}

// RequestLanding starts a change's landing: launch the run, audit. A landing
// takes no holder; it is refused while a run or a desktop checkout holds the
// change (a checkout's push would move the branch under its gates). Shared by the change page and the change chat —
// starting is not shipping; the run's gates and its human approval stand
// between this call and main.
func (s *Service) RequestLanding(ctx context.Context, p *project.Project, ch *workflow.Change, who string) (*workflow.Run, error) {
	run, err := s.work.RequestLanding(ctx, ch.ID)
	if err != nil {
		return nil, err
	}
	audit.At(s.now()).BestEffort(ctx, s.db, who, p.ID, "change.landing", fmt.Sprintf("change %d run %d", ch.ID, run.ID), nil)
	return run, nil
}

// RecordIntent attaches optional provenance after successful run admission.
// Like the existing request path, attachment/notice failure cannot fail creation.
// The run is admitted: its provenance is written though the caller has gone.
func (s *Service) RecordIntent(ctx context.Context, runID ids.ID, intent, by string) {
	if strings.TrimSpace(intent) == "" {
		return
	}
	if id, err := workflow.AddArtifactIn(context.WithoutCancel(ctx), s.db, runID, "intent", "intent", "", intent, s.now()); err == nil {
		workflow.RecordNoticeOrLog(context.WithoutCancel(ctx), s.db, runID, "artifact.added", map[string]any{"kind": "intent", "id": id, "by": by}, s.now())
	}
}
func encodedJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// RecordConsoleInvocation records optional run provenance after successful
// admission. Failure to append a receipt or intent cannot undo that admission.
// The run is admitted: its provenance is written though the caller has gone.
func (s *Service) RecordConsoleInvocation(ctx context.Context, runID ids.ID, actor, via string) {
	workflow.RecordNoticeOrLog(context.WithoutCancel(ctx), s.db, runID, "run.invoked", map[string]any{"actor": actor, "via": via}, s.now())
}

// RecordConsoleIntent preserves the console-specific artifact name and attribution.
func (s *Service) RecordConsoleIntent(ctx context.Context, runID ids.ID, intent, via string) {
	if strings.TrimSpace(intent) == "" {
		return
	}
	ctx = context.WithoutCancel(ctx) // the run is admitted; its intent is kept
	if id, err := workflow.AddArtifactIn(ctx, s.db, runID, "intent", "console decision", "", intent, s.now()); err == nil {
		workflow.RecordNoticeOrLog(ctx, s.db, runID, "artifact.added", map[string]any{"kind": "intent", "id": id, "by": via}, s.now())
	}
}

// CreateAndStart creates a Change and starts a run of the given workflow for
// it, as whoever the context names: a trigger acting for AppTown, or a
// person. Workflow admits the change and its first run atomically.
func (s *Service) CreateAndStart(ctx context.Context, p *project.Project, typ, title, body, workflowName string) (*workflow.Change, *workflow.Run, error) {
	return s.work.CreateAndRun(ctx, workflow.InvokeChange{
		Change: workflow.ChangeDraft{ProjectID: p.ID, Type: typ, Title: title, Body: body}, Workflow: workflowName,
	})
}

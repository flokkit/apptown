package apptown

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/apptown/modelcall"

	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// The living narrative (design/subsystems/dev-loop.md §cockpit): the Change's "So far"
// is a short plain-prose note re-distilled from evidence as work proceeds —
// never a big-bang generation at the end. Distillation runs on the `fast`
// model slot after a root run finishes and after a change console session
// ends. Failed publication retains the previous summary and is logged; explicit
// refresh also returns the error. Already incurred model usage is independent.

// The builder has three bound effects: slot resolution, live source evidence and
// model completion. Persistence and publication remain private Work operations.
// Configure a value before use; tests do not replace a global provider or App.
type narrativeBuilder struct {
	work     *workflow.Service
	resolve  func(context.Context, ids.ID) (string, string, error)
	evidence func(context.Context, workflow.NarrativeSource) (narrativeEvidence, error)
	models   modelcall.Completer
}

// Empty is set only by a successful committed-tree comparison, never inferred
// from missing transcripts or empty model output.
type narrativeEvidence struct {
	Stat, Patch string // the committed diff
	Text        string // the chats' reports
	Empty       bool
}

func (b narrativeBuilder) Refresh(ctx context.Context, changeID ids.ID) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	defer func() {
		if err != nil {
			log.Printf("change %d: narrative: %v", changeID, err)
		}
	}()
	source, err := b.work.NarrativeSource(ctx, changeID)
	if err != nil {
		return err
	}
	ch := source.Change
	extra, err := b.evidence(ctx, source)
	if err != nil {
		return err
	}
	if extra.Empty {
		return b.work.PublishNarrative(ctx, workflow.NarrativePublication{Source: source, Draft: workflow.NarrativeDraft{Note: workflow.EmptyCommittedChange}, Model: "forge/committed-diff"})
	}
	source.Stat, source.Patch = extra.Stat, extra.Patch
	pattern, slot, err := b.resolve(ctx, ch.ProjectID)
	if err != nil {
		return fmt.Errorf("choosing the narrative's model: %w", err)
	}
	if pattern == "" {
		return nil // no fast model: the change keeps its committed facts only
	}
	// The gateway writes the call's spend, independent of publication.
	out, _, err := b.models.Ask(ctx, modelcall.Call{Purpose: "narrative", ProjectID: ch.ProjectID, Model: pattern, System: workflow.NarrativePrompt, User: workflow.NarrativeEvidence(source, extra.Text)})
	if err != nil {
		return err
	}
	draft, err := workflow.ParseNarrative(out)
	if err != nil {
		return err
	}
	return b.work.PublishNarrative(ctx, workflow.NarrativePublication{Source: source, Draft: draft, Slot: slot, Model: pattern})
}

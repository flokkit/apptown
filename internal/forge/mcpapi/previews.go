package mcpapi

import (
	"context"
	"fmt"
	"time"

	"flokkit.io/forge/internal/forge/preview"

	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/workflow"
)

type PreviewsDependencies struct {
	Scope Scope
	Work  *workflow.Service
	// Previews boots the change's preview; Preview says its state.
	Previews *preview.Service
	Preview  interface {
		State(context.Context, *workflow.Change) preview.Status
	}
	DB *database.DB
	// Now is the forge's clock, which stamps the tool's receipt (the real
	// clock when nil).
	Now func() time.Time
}
type previews struct {
	scope    Scope
	work     *workflow.Service
	previews *preview.Service
	preview  interface {
		State(context.Context, *workflow.Change) preview.Status
	}
	db  *database.DB
	now func() time.Time
}

func NewPreviews(deps PreviewsDependencies) []Tool {
	h := previews{scope: deps.Scope, work: deps.Work, previews: deps.Previews, preview: deps.Preview, db: deps.DB, now: deps.Now}
	return []Tool{{Name: "preview_change",
		order: 13, Description: "Boots the Change as it is now and returns the URL to open it at, so you can try the app before handing it off.", InputSchema: schemaObject(map[string]any{"id": schemaID("The Change's id.")}, "id"), command: ownerCommand("previews.previewChange", h.previewChange)}}
}
func (h previews) previewChange(c *Call, args map[string]any) (any, error) {
	ch, p, err := h.scope.changeForRead(h.work, c, args)
	if err != nil {
		return nil, err
	}
	ctx := c.Context
	out, err := h.previews.RequestChangePreview(ctx, preview.Selection{ChangeID: ch.ID})
	if err != nil {
		return nil, refuse("the preview did not boot: %v", err)
	}
	audit.At(clock.Or(h.now)()).BestEffort(ctx, h.db, c.actor(), p.ID, "change.preview", fmt.Sprintf("%d", ch.ID), map[string]any{"url": out.URL, "via": "mcp"})
	return map[string]any{"changeId": ch.ID, "previewUrl": out.URL, "commit": out.Revision, "state": h.preview.State(ctx, ch), "webUrl": c.changeURL(ch.ID)}, nil
}

package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/clock"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/audit"
	"flokkit.io/forge/internal/forge/changeactions"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/secret"
)

type Webhook struct {
	projects  *project.Projects
	secrets   *secret.Service
	lifecycle *projectlifecycle.Service
	actions   *changeactions.Service
	db        *database.DB
	dataDir   string
	now       func() time.Time // AppTown's clock, which dates a webhook's change title
}

func NewWebhook(projects *project.Projects, secrets *secret.Service, lifecycle *projectlifecycle.Service, actions *changeactions.Service, db *database.DB, dataDir string, now func() time.Time) Webhook {
	return Webhook{projects: projects, secrets: secrets, lifecycle: lifecycle, actions: actions, db: db, dataDir: dataDir, now: clock.Or(now)}
}

// Webhook: POST /api/v1/hooks/{slug}/{workflow}. Requires the workflow
// to declare `on: webhook` and the caller to present APPTOWN_WEBHOOK_TOKEN.
func (h Webhook) Webhook(w http.ResponseWriter, r *http.Request) {
	expected := h.secrets.Value(r.Context(), "APPTOWN_WEBHOOK_TOKEN")
	if expected == "" {
		WriteError(w, 403, "webhooks disabled: set APPTOWN_WEBHOOK_TOKEN in %s/secrets.env", h.dataDir)
		return
	}
	// The token travels in a header only: a query string lands in proxy
	// access logs and shell history. Compared in constant time.
	if r.URL.Query().Has("token") {
		WriteError(w, 403, "send the webhook token in the X-AppTown-Token header, not the URL")
		return
	}
	if provided := r.Header.Get("X-AppTown-Token"); subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		WriteError(w, 403, "bad token")
		return
	}
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	wfName := r.PathValue("workflow")
	rw, err := h.lifecycle.SelectedWorkflow(r.Context(), p, wfName)
	if err != nil {
		WriteError(w, 404, "%v", err)
		return
	}
	doc := rw.Doc
	if doc.Spec.On.Webhook == nil {
		WriteError(w, 422, "workflow %q does not declare `on: webhook`", wfName)
		return
	}

	raw, _ := io.ReadAll(io.LimitReader(r.Body, 32*1024))
	at := h.now()
	title := fmt.Sprintf("%s webhook @ %s", wfName, at.Format("2006-01-02 15:04"))
	body := string(raw)
	var payload map[string]any
	if json.Unmarshal(raw, &payload) == nil {
		if t, ok := payload["title"].(string); ok && t != "" {
			title = t
		}
		if pretty, err := json.MarshalIndent(payload, "", "  "); err == nil {
			body = string(pretty)
		}
	}

	item, run, err := h.actions.CreateAndStart(webhookContext(r.Context(), p.ID), p, "webhook", title, body, wfName)
	if err != nil {
		WriteError(w, 422, "%v", err)
		return
	}
	audit.At(at).BestEffort(r.Context(), h.db, "webhook", p.ID, "change.triggered", title,
		map[string]any{"changeId": item.ID, "runId": run.ID, "trigger": "webhook"})
	WriteJSON(w, 201, WebhookStarted{ChangeID: item.ID, RunID: run.ID})
}

// webhookContext attributes a request only after the webhook credential check.
func webhookContext(ctx context.Context, projectID ids.ID) context.Context {
	return access.WithCaller(ctx, access.Caller{Principal: access.Principal{Kind: "instrument", Name: "system", Instrument: access.SystemInstrument, Via: "webhook", ProjectID: projectID}, Verified: true, Receipt: "system"})
}

// WebhookStarted names the Change and run a webhook started.
type WebhookStarted struct {
	ChangeID ids.ID `json:"changeId"`
	RunID    ids.ID `json:"runId"`
}

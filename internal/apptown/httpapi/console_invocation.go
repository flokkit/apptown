package httpapi

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/flokkit/apptown/internal/apptown/audit"
	"github.com/flokkit/apptown/internal/apptown/changeactions"
	"github.com/flokkit/apptown/internal/apptown/conversation"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/clock"
	"github.com/flokkit/apptown/internal/console"
	"github.com/flokkit/apptown/internal/database"
)

type ConsoleInvocation struct {
	sessions   *console.Service
	projects   *project.Projects
	ways       *wow.Service
	work       *workflow.Service
	actions    *changeactions.Service
	db         *database.DB
	publicHost string
	now        func() time.Time // AppTown's clock, which stamps its receipts; nil is the real one
}

func NewConsoleInvocation(sessions *console.Service, projects *project.Projects, ways *wow.Service, work *workflow.Service, actions *changeactions.Service, db *database.DB, publicHost string, now func() time.Time) ConsoleInvocation {
	return ConsoleInvocation{sessions: sessions, projects: projects, ways: ways, work: work, actions: actions, db: db, publicHost: publicHost, now: now}
}

// ConsoleInvoke is a project console's single sanctioned mutation:
// create a change and start its run on the session's OWN project, with an
// intent artifact. Reachable through the GET-only port; authenticated by the
// session token, not by network position. POST /console/{sid}/invoke
func (h ConsoleInvocation) ConsoleInvoke(w http.ResponseWriter, r *http.Request) {
	s := consoleForTokenRequest(h.sessions, w, r)
	if s == nil {
		return
	}
	// A change console is scoped BY CONSTRUCTION to its own change: its one
	// job is editing /work; opening sibling changes is how work escapes the
	// conversation that asked for it.
	if !conversation.Of(s).ChangeID.IsZero() {
		WriteError(w, 403, "this chat is scoped to change #%d — it cannot open new changes. Do the work directly in /work; if the operator wants a separate task, they can start it from the project page.", conversation.Of(s).ChangeID)
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "title and workflow required (or a catalog change name)"}, func(in struct {
		Title    string            `json:"title"`
		Body     string            `json:"body"`
		Type     string            `json:"type"`
		Workflow string            `json:"workflow"`
		Intent   string            `json:"intent"`
		Catalog  string            `json:"catalog"` // apply a catalog change instead of a raw workflow
		Params   map[string]string `json:"params"`
	}) {
		if in.Catalog == "" && (strings.TrimSpace(in.Title) == "" || in.Workflow == "") {
			WriteError(w, 400, "title and workflow required (or a catalog change name)")
			return
		}
		if in.Type == "" {
			in.Type = "feature"
		}
		// The change's body is the implementing agent's brief; a helper that
		// passed only --intent still wrote one, in the wrong field.
		if strings.TrimSpace(in.Body) == "" {
			in.Body = strings.TrimSpace(in.Intent)
		}
		p, err := h.projects.BySlug(r.Context(), conversation.Of(s).Slug)
		if err != nil {
			writeReadFault(w, err, "no such project: the chat's project is gone")
			return
		}
		var item *workflow.Change
		var run *workflow.Run
		ctx := conversation.Context(r.Context(), s)
		if in.Catalog != "" {
			c, cerr := h.ways.CatalogChange(in.Catalog)
			if cerr != nil {
				writeCatalogFault(w, cerr)
				return
			}
			item, run, err = h.actions.ApplyCatalog(ctx, &p, c, in.Params, s.Actor)
		} else {
			item, run, err = h.work.CreateAndRun(ctx, workflow.InvokeChange{Change: workflow.ChangeDraft{ProjectID: p.ID, Type: in.Type, Title: in.Title, Body: in.Body}, Workflow: in.Workflow})
		}
		if err != nil {
			WriteError(w, 400, "%s", err.Error())
			return
		}
		via := "console/" + s.ID
		h.actions.RecordConsoleInvocation(ctx, run.ID, s.Actor, via)
		// The chat's list of changes it started is display: a failure leaves
		// this change off the chat's list, never off the change.
		if err := conversation.RecordStarted(r.Context(), h.sessions, s, item.ID); err != nil {
			log.Printf("chat %s: noting it started change %d: %v", s.ID, item.ID, err)
		}
		h.actions.RecordConsoleIntent(ctx, run.ID, in.Intent, via)
		audit.At(clock.Or(h.now)()).BestEffort(ctx, h.db, s.Actor+" via "+via, p.ID, "console.invoke", fmt.Sprintf("run-%d", run.ID),
			map[string]any{"workflow": in.Workflow, "title": in.Title, "catalog": in.Catalog})
		// The link is part of the contract: the front desk hands the operator
		// a pointer to what it started.
		WriteJSON(w, 201, InvokedChange{CreatedChange: CreatedChange{Change: *item, Run: run}, URL: changeactions.URL(h.publicHost, item.ID)})
	})
}

// InvokedChange is a Change the front desk started, with its page's address.
type InvokedChange struct {
	CreatedChange
	URL string `json:"url"`
}

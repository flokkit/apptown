package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/modelcall"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/workflow"
	"github.com/flokkit/apptown/internal/database"
)

type SimpleChanges struct {
	projects   *project.Projects
	access     *identity.Service
	work       *workflow.Service
	adoption   *adoption.Service
	selection  ModelSelection
	completion modelcall.Completer
	db         *database.DB
	authMode   string
}

func NewSimpleChanges(projects *project.Projects, access *identity.Service, work *workflow.Service, adoption *adoption.Service, selection ModelSelection, completion modelcall.Completer, db *database.DB, authMode string) SimpleChanges {
	return SimpleChanges{projects: projects, access: access, work: work, adoption: adoption, selection: selection, completion: completion, db: db, authMode: authMode}
}
func (h SimpleChanges) CreateChangeSimple(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	if !RequireProjectMember(h.access, h.authMode, w, r, p.ID, p.Slug) {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(in struct {
		Text string `json:"text"`
	}) {
		text := strings.TrimSpace(in.Text)
		if text == "" {
			WriteError(w, 400, "say what you want changed — one sentence is enough")
			return
		}
		// Choosing the title and lane may spend on a model call: whoever may
		// not open and start the change is refused before anything is spent.
		if err := h.work.MayCreateAndRun(r.Context(), p.ID); err != nil {
			WriteFault(w, err)
			return
		}

		// Without the project's lanes the fallback would pick one it
		// may not have, so a failed read fails the request.
		available, err := h.adoption.AvailableWorkflows(r.Context(), p.ID)
		if err != nil {
			WriteFault(w, err)
			return
		}
		installed := map[string]bool{}
		for _, wf := range available {
			installed[wf.Name] = true
		}

		title, typ, lane, rationale := simpleFallback(text, installed)
		inferred := false
		if pattern, _, _ := h.selection.Resolve(r.Context(), p.ID, "fast"); pattern != "" && h.completion != nil {
			var opts strings.Builder
			for _, lc := range workflowChoices {
				if installed[lc.name] {
					fmt.Fprintf(&opts, "- %s: %s\n", lc.name, lc.when)
				}
			}
			system := `You triage a change request for a software project. From the user's text,
respond with ONLY a JSON object:
{"title": "<imperative, under 60 chars, plain words>",
 "type": "feature"|"bug"|"chore",
 "lane": "<one lane name from the list>",
 "why": "<one short sentence: why this lane>"}
Workflows:
` + opts.String()
			// Triage comes before the run exists: its spend is the project's,
			// with no run, even when the change is then not created.
			out, _, err := h.completion.Ask(r.Context(), modelcall.Call{Purpose: "simple-change", ProjectID: p.ID, Model: pattern, System: system, User: text})
			if err == nil {
				clean := strings.TrimSpace(out)
				clean = strings.TrimPrefix(clean, "```json")
				clean = strings.TrimPrefix(clean, "```")
				clean = strings.TrimSuffix(clean, "```")
				var v struct{ Title, Type, Workflow, Why string }
				if json.Unmarshal([]byte(strings.TrimSpace(clean)), &v) == nil &&
					v.Title != "" && installed[v.Workflow] {
					title, lane, rationale, inferred = v.Title, v.Workflow, v.Why, true
					if v.Type == "feature" || v.Type == "bug" || v.Type == "chore" {
						typ = v.Type
					}
				}
			}
		}

		if lane == "" {
			names := make([]string, len(workflowChoices))
			for i, c := range workflowChoices {
				names[i] = c.name
			}
			WriteError(w, 422, "this project has none of the simple lanes selected (%s) — apply a pack, or use the detailed form to pick a lane", strings.Join(names, ", "))
			return
		}
		item, run, err := h.work.CreateAndRun(r.Context(), workflow.InvokeChange{Change: workflow.ChangeDraft{ProjectID: p.ID, Type: typ, Title: title, Body: text}, Workflow: lane})
		if err != nil {
			WriteError(w, 422, "%v", err)
			return
		}
		intent := fmt.Sprintf("One-box change. Title, type and lane chosen %s: %s → %s (%s).",
			map[bool]string{true: "by the fast model", false: "by fallback rules (no model available)"}[inferred],
			title, lane, rationale)
		if id, err := workflow.AddArtifactIn(context.WithoutCancel(r.Context()), h.db, run.ID, "intent", "intent", "", intent, h.work.Now()); err == nil {
			workflow.RecordNoticeOrLog(context.WithoutCancel(r.Context()), h.db, run.ID, "artifact.added", map[string]any{"kind": "intent", "id": id, "by": Actor(r)}, h.work.Now())
		}
		WriteJSON(w, 201, SimpleChange{Change: *item, Run: *run,
			Picked: SimpleChoice{Title: title, Type: typ, Workflow: lane, Why: rationale}})
	})
}

var workflowChoices = []struct{ name, when string }{
	{"fix-bug", "something that used to work is broken, or behaves wrongly"},
	{"quick-change", "a small tweak, addition, or content edit to the existing app"},
	{"implement", "a new application or a substantial new capability built from a description"},
	{"plan-execute", "a broad or vague goal that needs breaking down before building"},
}

// simpleFallback derives title/type/lane without a model: first clause as
// the title, keyword sniff for bug-ness, quick-change (or the first
// installed choice) as the workflow.
func simpleFallback(text string, installed map[string]bool) (title, typ, workflow, why string) {
	title = text
	for _, cut := range []string{". ", "\n", "; "} {
		if i := strings.Index(title, cut); i > 0 {
			title = title[:i]
		}
	}
	if len(title) > 60 {
		title = strings.TrimSpace(title[:57]) + "..."
	}
	typ = "feature"
	lower := strings.ToLower(text)
	for _, kw := range []string{"broken", "doesn't work", "does not work", "error", "crash", "wrong", "bug", "used to"} {
		if strings.Contains(lower, kw) {
			typ = "bug"
			break
		}
	}
	workflow = ""
	prefer := []string{"quick-change", "fix-bug", "implement", "plan-execute"}
	if typ == "bug" {
		prefer = []string{"fix-bug", "quick-change", "implement", "plan-execute"}
	}
	for _, name := range prefer {
		if installed[name] {
			workflow = name
			break
		}
	}
	return title, typ, workflow, "fallback rules"
}

// SimpleChange is a one-box change and what was chosen for it, and why.
type SimpleChange struct {
	Change workflow.Change `json:"change"`
	Run    workflow.Run    `json:"run"`
	Picked SimpleChoice    `json:"picked"`
}

// SimpleChoice is the title, type and lane picked for a one-box change.
type SimpleChoice struct {
	Title    string `json:"title"`
	Type     string `json:"type"`
	Workflow string `json:"workflow"`
	Why      string `json:"why"`
}

package httpapi

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/wow"
	"github.com/flokkit/apptown/internal/fault"
)

type WorkflowDrafts struct {
	projects *project.Projects
	ways     *wow.Service
	adoption *adoption.Service
	draft    WorkflowDrafter
}

func NewWorkflowDrafts(projects *project.Projects, ways *wow.Service, adoption *adoption.Service, draft WorkflowDrafter) WorkflowDrafts {
	return WorkflowDrafts{projects: projects, ways: ways, adoption: adoption, draft: draft}
}

// AssistWorkflow: POST /api/v1/assist/workflow
// {projectSlug, description, currentYaml?} → drafts (or modifies) a workflow
// with the local model, validating server-side with one retry.
func (h WorkflowDrafts) AssistWorkflow(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{errorText: "description required"}, func(in struct {
		ProjectSlug string `json:"projectSlug"`
		Description string `json:"description"`
		CurrentYAML string `json:"currentYaml"`
	}) {
		if strings.TrimSpace(in.Description) == "" {
			WriteError(w, 400, "description required")
			return
		}

		// Project context: existing workflows + available skill/resource blocks.
		var ctx strings.Builder
		if in.ProjectSlug != "" {
			if p, err := h.projects.BySlug(r.Context(), in.ProjectSlug); err == nil {
				{
					// Optional: only a hint to the model's prompt, never shown
					// to a person as the project's state.
					available, err := h.adoption.AvailableWorkflows(r.Context(), p.ID)
					if err != nil {
						log.Printf("workflow draft for %s: reading its workflows for the prompt: %v", p.Slug, err)
					}
					var names []string
					for _, wf := range available {
						names = append(names, wf.Name)
					}
					if len(names) > 0 {
						ctx.WriteString("Existing workflows in this project (avoid name collisions unless modifying): " +
							strings.Join(names, ", ") + "\n")
					}
				}
			}
		}
		if blocks, err := h.ways.BlockNames(r.Context(), "skill/", "resource/"); err == nil {
			if len(blocks) > 0 {
				ctx.WriteString("Available skills/resources: " + strings.Join(blocks, ", ") + "\n")
			}
		}

		prompt := "Request: " + in.Description
		if strings.TrimSpace(in.CurrentYAML) != "" {
			prompt = "Modify the following workflow per the request. Output the COMPLETE updated YAML.\n\nRequest: " +
				in.Description + "\n\nCurrent workflow:\n" + in.CurrentYAML
		}

		yamlOut, attempts, err := h.draft.Draft(r.Context(), ctx.String(), prompt, Principal(r), Actor(r))
		if fault.KindOf(err) == fault.Forbidden {
			WriteFault(w, err)
			return
		} else if err != nil {
			WriteError(w, 502, "%v", err)
			return
		}
		WriteJSON(w, 200, WorkflowDraft{YAML: yamlOut, Attempts: attempts})
	})
}

// WorkflowDrafter drafts as the authenticated principal; receipt is the
// request's attribution, never its authority.
type WorkflowDrafter interface {
	Draft(ctx context.Context, about, prompt string, by access.Principal, receipt string) (string, int, error)
}

// WorkflowDraft is a drafted workflow and how many tries it took.
type WorkflowDraft struct {
	YAML     string `json:"yaml"`
	Attempts int    `json:"attempts"`
}

package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/access"
	"github.com/flokkit/apptown/internal/apptown/identity"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/wow"
)

type SystemWorkflows struct {
	ways   *wow.Service
	access *identity.Service
}

func NewSystemWorkflows(ways *wow.Service, access *identity.Service) SystemWorkflows {
	return SystemWorkflows{ways: ways, access: access}
}

// SystemWorkflows: GET /api/v1/system-workflows — each stock system
// workflow, what the instance runs for it, and the library's custom
// workflows that could stand in.
func (h SystemWorkflows) SystemWorkflows(w http.ResponseWriter, r *http.Request) {
	out := []SystemWorkflowBinding{}
	for _, s := range wow.StockSystemWorkflows {
		name := h.ways.SystemWorkflow(r.Context(), s)
		desc := ""
		if _, y, err := h.ways.WorkflowVersion(r.Context(), name, 0); err == nil {
			if d, err := schema.ParseWorkflowStored([]byte(y)); err == nil {
				desc = d.Metadata.Description
			}
		}
		out = append(out, SystemWorkflowBinding{Stock: s, Name: name, Custom: name != s, Description: desc})
	}
	candidates, err := h.ways.CustomWorkflowNames(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, SystemWorkflowBindings{Bindings: out, Candidates: candidates})
}

// PutSystemWorkflow: PUT /api/v1/system-workflows {stock, name} —
// name "" or equal to stock restores the stock workflow. Admin; audited.
func (h SystemWorkflows) PutSystemWorkflow(w http.ResponseWriter, r *http.Request) {
	if _, ok := AuthorizeRequest(h.access, w, r, access.CapExecWorkflows, access.Resource{Type: "instance"}, access.Context{}); !ok {
		return
	}
	jsonCommand(w, r, jsonBody{errorText: "stock and name required"}, func(in struct{ Stock, Name string }) {
		if in.Stock == "" {
			WriteError(w, 400, "stock and name required")
			return
		}

		if in.Name == "" || in.Name == in.Stock {
			if err := h.ways.ResetSystemWorkflow(r.Context(), in.Stock, actor(r)); err != nil {
				WriteFault(w, err)
				return
			}
			WriteJSON(w, 200, Done{OK: true})
			return
		}
		if err := h.ways.BindSystemWorkflow(r.Context(), in.Stock, in.Name, actor(r)); err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, Done{OK: true})
	})
}

// SystemWorkflowBinding is a stock system workflow and what the instance runs
// for it.
type SystemWorkflowBinding struct {
	Stock       string `json:"stock"`
	Name        string `json:"name"`
	Custom      bool   `json:"custom"`
	Description string `json:"description"`
}

// SystemWorkflowBindings is every system workflow's binding and the custom
// workflows that could stand in.
type SystemWorkflowBindings struct {
	Bindings   []SystemWorkflowBinding `json:"bindings"`
	Candidates []string                `json:"candidates"`
}

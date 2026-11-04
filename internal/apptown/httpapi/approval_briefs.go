package httpapi

import (
	"net/http"

	"github.com/flokkit/apptown/internal/apptown/briefs"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

type ApprovalBriefs struct {
	work   *workflow.Service
	briefs *briefs.Service
}

func NewApprovalBriefs(work *workflow.Service, service *briefs.Service) ApprovalBriefs {
	return ApprovalBriefs{work: work, briefs: service}
}
func (h ApprovalBriefs) ApprovalBrief(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	brief, err := h.briefs.Approval(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, ApprovalBrief{Brief: brief})
}
func (h ApprovalBriefs) FailureAdvice(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	advice, err := h.briefs.Failure(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, FailureAdvice{Text: advice.Text, Recommend: advice.Recommend})
}

// ApprovalQuestion: GET /api/v1/steps/{id}/question — what approving the
// step agrees to, for a client to show before it sends the decision.
func (h ApprovalBriefs) ApprovalQuestion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	question, err := h.work.ApprovalQuestion(r.Context(), id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, question)
}

// ApprovalBrief contains model prose shown to an approver, or an empty Brief
// when no model is configured. Generation failures return an HTTP error.
type ApprovalBrief struct {
	Brief string `json:"brief"`
}

// FailureAdvice is optional guidance beside a check's recorded words.
type FailureAdvice struct {
	Text      string `json:"text"`
	Recommend string `json:"recommend"`
}

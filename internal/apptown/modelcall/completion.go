// Package modelcall is the vocabulary of AppTown's own model calls:
// narratives, approval briefs, pack assistance, checks and
// the eval judge. The inference gateway answers them (inference.Gateway.Ask)
// through the same provider resolution and admission as a runtime's request,
// and writes their one spend line. Callers select a model and keep their
// fallback, publication and budget policies; they never record spend.
package modelcall

import "github.com/flokkit/apptown/internal/ids"

import "context"

// Call is one single-turn question AppTown asks for itself.
type Call struct {
	// Purpose names what the call is for (narrative, approval-brief,
	// pack-assistance, guidance-checklist,
	// conformance-review, what-if, eval-score); it is the spend line's kind.
	Purpose string
	// ProjectID is the project the call is made for, 0 for none. A call for
	// a project is admitted by the eval model choice and charged to it.
	ProjectID, RunID ids.ID
	// Actor is the spend line's actor; "" is AppTown itself.
	Actor string
	// Judge sends the call through the judge's door: an eval comparison's
	// rubric, admitted only for the copy's recorded judge. Set only by the
	// comparison's wiring, never from a request.
	Judge bool
	// Model is "<binding>/<model id>", as a model slot names it.
	Model, System, User string
}

// Completer answers AppTown's own calls. Once a call may have reached the
// provider its answer carries a Usage, even when the call failed; a refusal
// before sending carries none.
type Completer interface {
	Ask(ctx context.Context, call Call) (string, *Usage, error)
}

// Usage is what one call (or the retries of one question) cost, as the
// provider reported it.
type Usage struct {
	// The JSON names are those of the evidence already recorded.
	Prompt     int64   `json:"prompt_tokens"`
	Completion int64   `json:"completion_tokens"`
	Cost       float64 `json:"cost"`
	// Model is the identifier the provider reports it actually served — the
	// exact model behind an answer, not the mutable slot pattern that asked.
	Model string `json:"-"`
	// Incomplete says why the spend may be more than these numbers show: a
	// call that ended after it was sent, an answer with no usage, or tokens
	// with no price. "" when the provider reported the call whole.
	Incomplete string `json:"-"`
}

// Add folds a second call's spend into u (a retry of the same question):
// tokens and cost add up, and an incomplete call leaves the sum incomplete.
func (u *Usage) Add(o *Usage) {
	if o == nil {
		return
	}
	u.Prompt += o.Prompt
	u.Completion += o.Completion
	u.Cost += o.Cost
	if u.Incomplete == "" {
		u.Incomplete = o.Incomplete
	}
}

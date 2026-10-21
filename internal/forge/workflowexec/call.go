package workflowexec

import (
	"encoding/json"
	"fmt"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/database"
	"flokkit.io/forge/internal/forge/project"

	"flokkit.io/forge/internal/forge/workflow"
)

// workflow/call (design/subsystems/composition.md §2): a step whose body is another
// project workflow. The child is a REAL run — own ledger, own steps — but it
// runs inside the parent's story: same work item, same workspace/branch
// (the source observation resolves through the root run), no merge of
// its own. The call step's verdict is the child's outcome; the parent's
// evidence pins {childRunId, workflow, workflowVersion}. Version pinned at
// invocation; approvals in the child bubble up through runState.

// workflow.MaxCallDepth caps the parent chain (root counts as depth 1): a backstop
// for recursive calls, including cycles introduced by later edits.

const CallKind = "workflow/call"

// Calls prepares invocation inputs; Workflow owns atomic child admission.
type Calls struct {
	db     *database.DB
	panics interface{ Panic(string, any, []byte) }
}

func NewCalls(db *database.DB, panics interface{ Panic(string, any, []byte) }) *Calls {
	return &Calls{db: db, panics: panics}
}

// Run executes a workflow/call step: resolve the callee at its
// current version, create a pinned child run in the parent's context, and
// return — the child drives itself (pipelines auto-advance); completion
// records the verdict back on this step.
func (a *Calls) Run(task *workflow.Execution) {
	defer reportPanic(a.panics, "RunWorkflowCall")
	step := task.Step()
	parent := task.Run()
	var cfg callConfig
	cfgErr := workflow.DecodeStepConfig(step.Config, &cfg)

	fail := func(reason string) {
		if _, claimErr := task.Claim(); claimErr == nil {
			task.Settle(workflow.EncodeOutcome(workflow.StatusError, map[string]any{"reason": reason}, nil))
		}
	}
	if cfgErr != nil {
		fail(cfgErr.Error())
		return
	}
	if cfg.Workflow == "" {
		fail("workflow/call needs with.workflow")
		return
	}
	source, err := workflow.ReadSourceIn(task.Context(), a.db, workflow.RunRef{ID: parent.ID, ProjectID: parent.ProjectID})
	if err != nil {
		fail(err.Error())
		return
	}
	if source.Depth >= workflow.MaxCallDepth {
		fail(fmt.Sprintf("call depth cap (%d) reached — check for workflow cycles", workflow.MaxCallDepth))
		return
	}
	if _, err := project.ByIDIn(task.Context(), a.db, parent.ProjectID); err != nil {
		fail("project gone")
		return
	}

	// Inputs: interpolated in the PARENT's context, frozen into the child.
	var item *workflow.Change
	if !parent.ChangeID.IsZero() {
		item, _ = workflow.ReadChangeIn(task.Context(), a.db, parent.ChangeID)
	}
	slug := source.Slug
	inputs := map[string]string{}
	for k, v := range cfg.With {
		if k == "workflow" {
			continue
		}
		inputs[k] = workflow.Interpolate(task.Context(), a.db, v, parent, item, slug)
	}

	principal := access.Principal{Kind: "instrument", Name: fmt.Sprintf("run/%d", parent.ID), Instrument: "run", Via: "run", ProjectID: parent.ProjectID}
	caller := access.WithCaller(task.Context(), access.Caller{Principal: principal, Verified: true, Receipt: principal.Name})
	body, _ := json.Marshal(inputs)
	child, err := task.Call(caller, cfg.Workflow, body)
	if err != nil && child == nil {
		fail("creating child run: " + err.Error())
	}
	// Once committed, the child link and running evidence already exist.
	// A preparation/dispatch failure leaves that child recoverable; never
	// overwrite its parent evidence after its runners may have completed.
}

// callConfig is a workflow/call step's options: the workflow to run and
// the inputs passed to it.
type callConfig struct {
	Workflow string            `json:"workflow"`
	With     map[string]string `json:"with"`
}

// Package workcommands composes preparation and effects around Workflow commands.
package workcommands

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokkit/apptown/internal/ids"

	"github.com/flokkit/apptown/internal/clock"

	"github.com/flokkit/apptown/internal/apptown/project"
	"github.com/flokkit/apptown/internal/apptown/repositoryedit"
	"github.com/flokkit/apptown/internal/database"

	"github.com/flokkit/apptown/internal/apptown/adoption"
	"github.com/flokkit/apptown/internal/apptown/schema"
	"github.com/flokkit/apptown/internal/apptown/scm"
	"github.com/flokkit/apptown/internal/apptown/workflow"
)

// adoptedWorkflows resolves procedure and guidance from one recorded adoption.
// Live policy checks remain admission rules, not part of that frozen receipt.
type adoptedWorkflows interface {
	Workflow(context.Context, adoption.WorkflowRequest) (*adoption.ResolvedWorkflow, error)
	Resolve(context.Context, adoption.Source) (adoption.Resolved, error)
	CheckWorkflow(context.Context, database.Queryer, ids.ID, string, *schema.Spec) ([]adoption.Violation, error)
}

// Preparation builds an admission definition without owning the run transaction
// or its dispatcher. Ordinary, nested and remediation starts use the same path.
type Preparation struct {
	db       *database.DB
	adoption adoptedWorkflows
	branches *scm.Branches
	dataDir  string
	chats    workflow.Presence
	workers  workerChats
	clock    func() time.Time // AppTown's clock; nil is the real one
	stores   secretStores
}

// secretStores says which secrets stores are available when a run is
// admitted, by provider name; nil records none. A secrets file that cannot
// be read is an error.
type secretStores interface {
	AvailableProviders() (map[string]bool, error)
}

func (a *Preparation) now() time.Time { return clock.Or(a.clock)() }

// workerChats saves a chat on a worker, so what a landing, recheck or
// `apptown check` chooses is the chat's work as saved on the branch
// (design/operations/workers.md, "Chats on the worker").
type workerChats interface {
	// SaveFirst saves the chat that holder names when it runs on
	// a worker, and answers false (having done nothing) when it does not.
	// A save that is not confirmed, or refused, is the error; so is not
	// knowing where the chat runs.
	SaveFirst(ctx context.Context, holder string) (onWorker bool, err error)
	// FollowFirst gives the chat on a worker that holder names the commit
	// to (a descendant of from, the branch's tip it saved last) in its work
	// tree, then calls publish, both under the chat's save lock, so no save
	// moves the branch between them. A tree that cannot take it is the
	// error, and publish is not called.
	FollowFirst(ctx context.Context, holder, hub, from, to string, publish func() error) error
	// Tell sends the chat that holder names a message, best effort.
	Tell(ctx context.Context, holder, text string)
}

func NewPreparation(db *database.DB, adopted adoptedWorkflows, branches *scm.Branches, dataDir string, chats workflow.Presence, workers workerChats, stores secretStores, now func() time.Time) *Preparation {
	return &Preparation{db: db, adoption: adopted, branches: branches, dataDir: dataDir, chats: chats, workers: workers, stores: stores, clock: now}
}

// Resolve prepares immutable receipts. Only the start store writes
// the run, its relationships, and its first stage; only the starter orders
// workspace preparation and dispatch after that transaction commits.
func (a *Preparation) Resolve(ctx context.Context, req workflow.StartRequest, source workflow.StartSource) (workflow.PreparedRun, error) {
	p, projectErr := project.ByIDIn(ctx, a.db, req.ProjectID)
	if projectErr != nil {
		return workflow.PreparedRun{}, fmt.Errorf("project %d is unavailable", req.ProjectID)
	}
	ad, err := adoption.ProjectAdoptionIn(ctx, a.db, req.ProjectID, source.AdoptionID)
	if err != nil {
		return workflow.PreparedRun{}, err
	}
	rw, err := a.adoption.Workflow(ctx, adoption.WorkflowRequest{Source: adoption.Source{ProjectID: p.ID, Revision: ad}, Name: req.WorkflowName, ProjectSlug: p.Slug})
	if err != nil {
		return workflow.PreparedRun{}, err
	}
	// A landing is admitted under candidate pinning: the marker makes its
	// gates and merge wait for a candidate, and never fall back to the live
	// workspace. When the branch exists now, the candidate is pinned here;
	// otherwise it is pinned once the run's workspace is prepared.
	var pinning []workflow.StartEvent
	if !req.IsChild() && rw.Doc.Spec.Role == "landing" && p.DefaultBranch != "" {
		pinning = append(pinning, workflow.LandingPinRequired())
		if source.Change != nil {
			// The change's committed work in its folder is published first:
			// under its chat's own grant when a chat holds the change, or
			// under a grant this request takes and at once releases when
			// nobody does, and main is merged in under the same grant. What
			// is not committed is not in this landing, and the record says
			// so; uncommitted work with nothing committed, or a main that
			// conflicts, refuses the landing. A run or a checkout holding
			// the change refuses the landing when it is admitted.
			// A landing AppTown starts on accepted work names no person:
			// AppTown asked for it.
			by := req.Requester()
			if by == "" {
				by = "apptown-system"
			}
			published, err := a.publishForLanding(ctx, &p, source.Change, by)
			if err != nil {
				return workflow.PreparedRun{}, err
			}
			if uncommitted := published.uncommitted; len(uncommitted) > 0 {
				body, _ := json.Marshal(map[string]any{"files": uncommitted, "reason": "not committed in the change's folder, so not in this landing"})
				pinning = append(pinning, workflow.StartEvent{Kind: "landing.uncommitted", Payload: body})
			}
			if published.merged != nil {
				body, _ := json.Marshal(mainMerged(source.Change, p.DefaultBranch, by, "admission", published.merged))
				pinning = append(pinning, workflow.StartEvent{Kind: repositoryedit.MainMergedNotice, Payload: body})
			}
			// The branch as published (main merged in, when it was) and
			// main are each read once, for the pinned candidate.
			hub, err := p.Repository(a.dataDir)
			if err != nil {
				return workflow.PreparedRun{}, err
			}
			tip, main := scm.Ref(ctx, hub, "refs/heads/"+source.Change.Branch), scm.Ref(ctx, hub, "refs/heads/"+p.DefaultBranch)
			pinned, err := admissionCandidate(tip, main)
			if err != nil {
				return workflow.PreparedRun{}, err
			}
			if pinned != nil {
				pinning = append(pinning, *pinned)
			}
		}
	}
	def, err := a.describe(ctx, req, workflow.DefinitionRef{Name: rw.Name, Version: rw.Version, Origin: rw.Origin}, &rw.Doc.Spec, ad)
	def.System = rw.System
	if err == nil {
		def.Events = append(def.Events, pinning...)
	}
	return def, err
}
func (a *Preparation) describe(ctx context.Context, req workflow.StartRequest, wf workflow.DefinitionRef, spec *schema.Spec, ad *adoption.RecordedRevision) (workflow.PreparedRun, error) {
	def := workflow.PreparedRun{Workflow: wf, Spec: *spec}
	emit := func(kind string, payload any) error {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		def.Events = append(def.Events, workflow.StartEvent{Kind: kind, Payload: body})
		return nil
	}
	violations, err := a.adoption.CheckWorkflow(ctx, a.db, req.ProjectID, wf.Name, spec)
	if err != nil {
		return def, err
	}
	for _, v := range violations {
		if err := emit("policy.warning", struct {
			Policy string `json:"policy"`
			Detail string `json:"detail"`
		}{v.Policy, v.Detail}); err != nil {
			return def, err
		}
	}
	if err := a.pinRunIntegrations(ctx, emit, req.ProjectID); err != nil {
		return def, err
	}
	if ad != nil {
		def.AdoptionID = ad.ID
		if err := emit("adoption.resolved", struct {
			AdoptionID ids.ID `json:"adoptionId"`
			Revision   int64  `json:"revision"`
			Digest     string `json:"digest"`
			Inherited  bool   `json:"inherited"`
		}{ad.ID, ad.Revision, ad.Digest, req.IsChild()}); err != nil {
			return def, err
		}
	}
	// Guidance is compiled from the same adoption used for workflow resolution.
	resolved, err := a.adoption.Resolve(ctx, adoption.Source{ProjectID: req.ProjectID, Revision: ad})
	hash, imports := resolved.Hash, resolved.Imports
	if err != nil {
		return def, err
	}
	if len(imports) > 0 {
		type pin struct {
			Name    string `json:"name"`
			Version int    `json:"version"`
		}
		var pins []pin
		for _, im := range imports {
			if !im.Disabled {
				pins = append(pins, pin{im.Name, im.Version})
			}
		}
		if err := emit("guidance.pinned", struct {
			Hash    string `json:"hash"`
			Imports []pin  `json:"imports"`
		}{hash, pins}); err != nil {
			return def, err
		}
	}
	return def, nil
}

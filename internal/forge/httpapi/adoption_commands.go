package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/access"
	"flokkit.io/forge/internal/fault"
	"flokkit.io/forge/internal/forge/adoption"
	"flokkit.io/forge/internal/forge/conformance"
	"flokkit.io/forge/internal/forge/identity"
	"flokkit.io/forge/internal/forge/project"
	"flokkit.io/forge/internal/forge/projectlifecycle"
	"flokkit.io/forge/internal/forge/verification"
	"flokkit.io/forge/internal/forge/workflow"
	"flokkit.io/forge/internal/strs"
)

type AdoptionCommands struct {
	adoption  *adoption.Service
	whatIfs   *conformance.Service
	projects  *project.Projects
	access    *identity.Service
	work      *workflow.Service
	lifecycle *projectlifecycle.Service
}

func NewAdoptionCommands(adoption *adoption.Service, whatIfs *conformance.Service, projects *project.Projects, access *identity.Service, work *workflow.Service, lifecycle *projectlifecycle.Service) AdoptionCommands {
	return AdoptionCommands{adoption: adoption, whatIfs: whatIfs, projects: projects, access: access, work: work, lifecycle: lifecycle}
}

// Prepare: POST /projects/{slug}/adoption/prepare {packs?, additions?}
func (h AdoptionCommands) Prepare(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{}, func(req adoption.Request) {
		c, err := h.adoption.Prepare(r.Context(), p.ID, req)
		if err != nil {
			WriteError(w, 422, "%v", err)
			return
		}
		WriteJSON(w, 200, c)
	})
}

// Adopt: POST /projects/{slug}/adoption/adopt {expectedRevision,
// packs?, additions?, fromWhatIf?, viaChange?} — adopts now, or opens the
// Change that will. Weakening a pack's obligation takes the standards
// authority. fromWhatIf adopts what that what-if assessed for this project,
// read from its record (pack, version, the other packs it moved with it,
// adoption revision, candidate), and
// is refused when that is no longer what the project would adopt; the
// Change's brief names the what-if.
func (h AdoptionCommands) Adopt(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	// Strict: a field this door does not know (the older move and
	// expectedDigest, from a page loaded before they were replaced) is
	// refused, never dropped into adopting the current selection instead.
	jsonCommand(w, r, jsonBody{strict: true}, func(in struct {
		adoption.Request
		ExpectedRevision int64  `json:"expectedRevision"`
		FromWhatIf       ids.ID `json:"fromWhatIf"`
		ViaChange        bool   `json:"viaChange"`
	}) {
		expectedDigest, basis := "", ""
		if !in.FromWhatIf.IsZero() {
			// Present at all, even empty, is a second source of what is adopted.
			if in.Packs != nil || in.Additions != nil || in.RequiredCapabilities != nil || in.ExpectedRevision != 0 {
				WriteError(w, 400, "a what-if says what is adopted; name it alone")
				return
			}
			assessed, err := h.assessedIn(r, in.FromWhatIf, p.ID)
			if err != nil {
				WriteFault(w, err)
				return
			}
			picks, err := h.adoption.PicksWith(r.Context(), p.ID, assessed.pack, assessed.version, assessed.also...)
			if err != nil {
				WriteFault(w, err)
				return
			}
			in.Request, in.ExpectedRevision, expectedDigest, basis = adoption.Request{Packs: picks, AdvanceWorkflows: assessed.advance}, assessed.revision, assessed.digest, assessed.basis
		}
		c, err := h.adoption.Prepare(r.Context(), p.ID, in.Request)
		if err != nil {
			WriteError(w, 422, "%v", err)
			return
		}
		if in.ExpectedRevision != 0 && c.ExpectedRevision != in.ExpectedRevision {
			WriteError(w, 409, "the project is on revision %d, not %d — prepare again", c.ExpectedRevision, in.ExpectedRevision)
			return
		}
		if expectedDigest != "" && c.Digest != expectedDigest {
			WriteError(w, 409, "what this project would adopt has changed since it was assessed — run the what-if again")
			return
		}
		if len(c.Problems) > 0 {
			WriteError(w, 422, "cannot be adopted as asked: %s", strings.Join(c.Problems, "; "))
			return
		}
		who := Actor(r)
		if len(c.Weakens) > 0 && !h.holds(r.Context(), Principal(r), access.CapStandardsPacks, access.Resource{Type: "instance"}) {
			WriteError(w, 403, "dropping %s weakens what a selected pack asks of this project; that takes the standards authority (%s)", strings.Join(c.Weakens, ", "), access.CapStandardsPacks)
			return
		}
		activity, err := h.work.ProjectActivity(r.Context(), p.ID)
		if err != nil {
			WriteFault(w, err)
			return
		}
		open := activity[p.ID].OpenUplifts
		if open > 0 {
			WriteError(w, 409, "an adoption change is already open on %s — land or abandon it first", p.Slug)
			return
		}
		// Going back is allowed, never silent: the Change's brief and the
		// revision's reason both say what moves back.
		back := ""
		if mb := c.MovesBack(); len(mb) > 0 {
			back = " Moves back: " + strings.Join(mb, ", ") + "."
		}
		if in.ViaChange {
			ch, err := h.adoption.OpenUplift(r.Context(), p.ID, *c, who, strings.TrimSpace(basis+back))
			if err != nil {
				WriteFaultOr(w, err, 422)
				return
			}
			WriteJSON(w, 200, AdoptionUpliftOpened{ChangeID: ch.ID, ChangeNumber: ch.Number, Candidate: *c, RunID: ch.Run, RunNumber: ch.RunNumber, Workflow: ch.Workflow, RunError: ch.RunError, NotStarted: ch.NotStarted})
			return
		}
		rev, err := h.lifecycle.Adopt(r.Context(), p, c, who, "adopted under Settings › Guidance: "+strings.Join(c.Words(), ", ")+"."+back, adoption.OperationSelect)
		if err != nil {
			WriteFaultOr(w, err, 409)
			return
		}
		WriteJSON(w, 200, AdoptionApplied{Revision: rev, Candidate: *c})
	})
}

// assessed is what a what-if assessed for one project: its pack and
// version, the adoption revision and candidate it prepared, and the line a
// Change's brief gives for it.
type assessed struct {
	pack              string
	version, revision int64
	digest, basis     string
	advance           bool
	also              []adoption.PackPick
}

// assessedIn reads what-if id's answer for the project. Reading it takes
// conformance read; a what-if not done, or with no candidate for the
// project, is no basis to adopt on.
func (h AdoptionCommands) assessedIn(r *http.Request, id, projectID ids.ID) (assessed, error) {
	wi, err := h.whatIfs.WhatIf(r.Context(), id)
	if err != nil {
		return assessed{}, err
	}
	if wi.State != conformance.WhatIfDone {
		return assessed{}, fault.New(fault.Conflict, fmt.Sprintf("what-if #%d is %s, not done; adopt from one that finished", id, wi.State))
	}
	for _, p := range wi.Projects {
		if p.ProjectID != projectID || p.CandidateDigest == "" {
			continue
		}
		basis := fmt.Sprintf("Assessed in what-if #%d at %s: %s.", wi.ID, strs.ShortSHA(p.SHA), conformance.WhatIfOutcomeWords(p.Outcome))
		return assessed{pack: wi.Pack, version: wi.Version, revision: p.AdoptionRevision, digest: p.CandidateDigest, basis: basis, advance: wi.AdvanceWorkflows, also: verification.AlsoPicks(wi.Also)}, nil
	}
	return assessed{}, fault.New(fault.Conflict, fmt.Sprintf("what-if #%d assessed no candidate for this project", id))
}

func (h AdoptionCommands) holds(ctx context.Context, pr access.Principal, c access.Capability, res access.Resource) bool {
	return h.access.Evaluate(ctx, pr, access.DelegationOf(pr), c, res, access.Context{}).Allow
}

// AdoptionUpliftOpened names the change that will carry an adoption, and
// what came of starting its working run: runId when a run was admitted
// (with runError when it then failed to get going), notStarted when none
// was. The change stands in every case.
type AdoptionUpliftOpened struct {
	ChangeID     ids.ID             `json:"changeId"`
	ChangeNumber int64              `json:"changeNumber"` // within the project
	Candidate    adoption.Candidate `json:"candidate"`
	RunID        ids.ID             `json:"runId,omitzero"`
	RunNumber    int64              `json:"runNumber,omitempty"`
	Workflow     string             `json:"workflow,omitempty"`
	RunError     string             `json:"runError,omitempty"`
	NotStarted   string             `json:"notStarted,omitempty"`
}

// AdoptionApplied is the revision an adoption wrote.
type AdoptionApplied struct {
	Revision  int64              `json:"revision"`
	Candidate adoption.Candidate `json:"candidate"`
}

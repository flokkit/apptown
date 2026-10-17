package httpapi

import (
	"context"
	"net/http"
	"strings"

	"flokkit.io/forge/internal/forge/dataprotection"
	"flokkit.io/forge/internal/ids"

	"flokkit.io/forge/internal/forge/operations"
	"flokkit.io/forge/internal/forge/project"
)

// DataProtection is the doors of protecting deployment data off-host
// (design/subsystems/deployment.md): the backup store, each
// deployment's protection, copies, drills and restore rehearsals on
// request, final copies to start from, and the data operations that do the
// long work.
type DataProtection struct {
	projects *project.Projects
	data     *dataprotection.Service
}

func NewDataProtection(projects *project.Projects, data *dataprotection.Service) DataProtection {
	return DataProtection{projects: projects, data: data}
}

// The data protection routes' responses.
type (
	// BackupStoreState is the attached backup store, or none.
	BackupStoreState struct {
		Store *dataprotection.BackupStore `json:"store"`
	}
	// FinalCopies are a project's final copies a new deployment can start from.
	FinalCopies struct {
		Copies []dataprotection.FinalCopy `json:"copies"`
	}
	DeploymentProtection = dataprotection.Protection
)

// Store: GET /backup-store.
func (h DataProtection) Store(w http.ResponseWriter, r *http.Request) {
	st, err := h.data.Store(r.Context())
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, BackupStoreState{Store: st})
}

// AttachStore: PUT /backup-store {endpoint, region, bucket, accessKeyFrom,
// secretKeyFrom} — checked, then attached.
func (h DataProtection) AttachStore(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{}, func(in dataprotection.AttachStore) {
		st, err := h.data.AttachStore(r.Context(), in)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, BackupStoreState{Store: st})
	})
}

// Protection: GET /projects/{slug}/deployments/{name}/protection.
func (h DataProtection) Protection(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	out, err := h.data.DeploymentProtection(r.Context(), p.ID, r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, DeploymentProtection(*out))
}

// Copy: POST /projects/{slug}/deployments/{name}/copies — a copy to the
// backup store now, as an operation.
func (h DataProtection) Copy(w http.ResponseWriter, r *http.Request) {
	h.start(w, r, h.data.RequestCopy)
}

// Drill: POST /projects/{slug}/deployments/{name}/drills — the newest copy
// filled into a new place and checked, as an operation.
func (h DataProtection) Drill(w http.ResponseWriter, r *http.Request) {
	h.start(w, r, h.data.RequestDrill)
}

// Rehearse: POST /projects/{slug}/deployments/{name}/rehearsals {backup?}
// — a restore rehearsal from the named copy (default: the newest), as an
// operation (design/subsystems/deployment.md, "Restore rehearsals").
func (h DataProtection) Rehearse(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{optional: true}, func(in struct {
		Backup ids.ID `json:"backup"`
	}) {
		h.start(w, r, func(ctx context.Context, project ids.ID, name string) (*operations.Operation, error) {
			return h.data.RequestRehearsal(ctx, project, name, in.Backup)
		})
	})
}

func (h DataProtection) start(w http.ResponseWriter, r *http.Request, begin func(context.Context, ids.ID, string) (*operations.Operation, error)) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	op, err := begin(r.Context(), p.ID, r.PathValue("name"))
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteAdmitted(w, op)
}

// FinalCopies: GET /projects/{slug}/final-copies.
func (h DataProtection) FinalCopies(w http.ResponseWriter, r *http.Request) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return
	}
	copies, err := h.data.FinalCopies(r.Context(), p.ID)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteJSON(w, 200, FinalCopies{Copies: copies})
}

// Restored copies (design/subsystems/deployment.md, "Restored copies"): a production
// deployment's release started from a copy on its own target, behind the
// forge's access gate.
type (
	// RestoredCopyState is one restored copy.
	RestoredCopyState = dataprotection.RestoredCopy
	// GateEntry is what a redeemed ticket lets a gate do.
	GateEntry = dataprotection.GateEntryGrant
)

// StartRestoredCopy: POST /projects/{slug}/deployments/{name}/restored-copies
// {backup?, minutes?, test?} — a restore test, or a copy people may open
// for a while, as an operation.
func (h DataProtection) StartRestoredCopy(w http.ResponseWriter, r *http.Request) {
	jsonCommand(w, r, jsonBody{optional: true, strict: true}, func(in dataprotection.RestoredCopyRequest) {
		h.start(w, r, func(ctx context.Context, project ids.ID, name string) (*operations.Operation, error) {
			return h.data.RequestRestoredCopy(ctx, project, name, in)
		})
	})
}

// ExtendRestoredCopy: POST /projects/{slug}/restored-copies/{id}/extend
// {minutes} — keep an open copy open until now plus minutes.
func (h DataProtection) ExtendRestoredCopy(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.restoredCopyPath(w, r)
	if !ok {
		return
	}
	jsonCommand(w, r, jsonBody{strict: true}, func(in struct {
		Minutes int64 `json:"minutes"`
	}) {
		c, err := h.data.ExtendRestoredCopy(r.Context(), p, id, in.Minutes)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, RestoredCopyState(*c))
	})
}

// TakeDownRestoredCopy: DELETE /projects/{slug}/restored-copies/{id} — take
// the copy down now, as an operation.
func (h DataProtection) TakeDownRestoredCopy(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.restoredCopyPath(w, r)
	if !ok {
		return
	}
	op, err := h.data.TakeDownRestoredCopy(r.Context(), p, id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	WriteAdmitted(w, op)
}

// OpenRestoredCopy: POST /projects/{slug}/restored-copies/{id}/open — a
// form a person submits from the forge in their browser. The answer is a
// redirect to the copy's gate carrying a one-use ticket; it is never a
// page a GET can produce, so another site cannot open a copy in a
// person's name.
func (h DataProtection) OpenRestoredCopy(w http.ResponseWriter, r *http.Request) {
	p, id, ok := h.restoredCopyPath(w, r)
	if !ok {
		return
	}
	to, err := h.data.OpenRestoredCopy(r.Context(), p, id)
	if err != nil {
		WriteFault(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (h DataProtection) restoredCopyPath(w http.ResponseWriter, r *http.Request) (ids.ID, ids.ID, bool) {
	p, ok := ProjectFromPath(h.projects, w, r)
	if !ok {
		return ids.Nil, ids.Nil, false
	}
	id, err := PathID(r, "id")
	if err != nil {
		WriteError(w, 400, "bad id")
		return ids.Nil, ids.Nil, false
	}
	return p.ID, id, true
}

// RedeemGateTicket: POST /gate/redeem {ticket}, from a restored copy's
// gate, which proves itself with its secret as a bearer credential. It
// answers who to let in and until when, or 403.
func (h DataProtection) RedeemGateTicket(w http.ResponseWriter, r *http.Request) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" {
		WriteError(w, 403, "not a restored copy's gate")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonCommand(w, r, jsonBody{strict: true, limit: 4 << 10}, func(in struct {
		Ticket string `json:"ticket"`
	}) {
		grant, err := h.data.RedeemGateTicket(r.Context(), secret, in.Ticket)
		if err != nil {
			WriteFault(w, err)
			return
		}
		WriteJSON(w, 200, GateEntry(grant))
	})
}
